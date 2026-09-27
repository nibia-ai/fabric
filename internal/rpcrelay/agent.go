package rpcrelay

import (
	"bufio"
	"context"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"fmt"
	"net"
	"os"
	"strconv"
	"strings"
	"time"
)

type AgentConfig struct {
	NodeID       string
	NodeName     string
	RelayAddress string
	CertFile     string
	KeyFile      string
	CAFile       string
	PoolSize     int
}

func DeriveRelayAddress(secureControllerURL string, relayPort int) (string, error) {
	if relayPort == 0 {
		relayPort = DefaultControllerPort
	}
	raw := strings.TrimSpace(secureControllerURL)
	raw = strings.TrimPrefix(raw, "https://")
	raw = strings.TrimPrefix(raw, "http://")
	host, _, err := net.SplitHostPort(raw)
	if err != nil {
		// Accept a bare host for tests/custom deployments.
		host = raw
	}
	host = strings.Trim(host, "[]")
	if host == "" {
		return "", fmt.Errorf("cannot derive relay host from secure controller URL")
	}
	return net.JoinHostPort(host, strconv.Itoa(relayPort)), nil
}

func RunAgentPool(ctx context.Context, cfg AgentConfig, logf func(string, ...any)) error {
	cfg.NodeID = strings.TrimSpace(cfg.NodeID)
	cfg.NodeName = strings.TrimSpace(cfg.NodeName)
	cfg.RelayAddress = strings.TrimSpace(cfg.RelayAddress)
	if cfg.NodeID == "" || cfg.RelayAddress == "" {
		return fmt.Errorf("relay node id and controller address are required")
	}
	if cfg.PoolSize <= 0 {
		cfg.PoolSize = 4
	}
	if cfg.PoolSize > 16 {
		cfg.PoolSize = 16
	}
	tlsConfig, err := agentTLSConfig(cfg)
	if err != nil {
		return err
	}
	sessionID, err := newRelaySessionID()
	if err != nil {
		return err
	}
	for i := 0; i < cfg.PoolSize; i++ {
		go relayWorker(ctx, cfg, tlsConfig, sessionID, i, logf)
	}
	<-ctx.Done()
	return nil
}

func agentTLSConfig(cfg AgentConfig) (*tls.Config, error) {
	cert, err := tls.LoadX509KeyPair(cfg.CertFile, cfg.KeyFile)
	if err != nil {
		return nil, err
	}
	caPEM, err := os.ReadFile(cfg.CAFile)
	if err != nil {
		return nil, err
	}
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(caPEM) {
		return nil, fmt.Errorf("invalid controller CA")
	}
	host, _, err := net.SplitHostPort(cfg.RelayAddress)
	if err != nil {
		return nil, err
	}
	return &tls.Config{
		MinVersion:   tls.VersionTLS13,
		RootCAs:      pool,
		Certificates: []tls.Certificate{cert},
		ServerName:   strings.Trim(host, "[]"),
	}, nil
}

func relayWorker(ctx context.Context, cfg AgentConfig, tlsConfig *tls.Config, sessionID string, slot int, logf func(string, ...any)) {
	if logf == nil {
		logf = func(string, ...any) {}
	}
	backoff := 500 * time.Millisecond
	hadReadySession := false
	for {
		if ctx.Err() != nil {
			return
		}
		dialer := &net.Dialer{Timeout: 5 * time.Second}
		raw, err := dialer.DialContext(ctx, "tcp", cfg.RelayAddress)
		if err != nil {
			sleepContext(ctx, backoff)
			if backoff < 5*time.Second {
				backoff *= 2
			}
			continue
		}
		conn := tls.Client(raw, tlsConfig.Clone())
		if err := conn.HandshakeContext(ctx); err != nil {
			_ = raw.Close()
			sleepContext(ctx, backoff)
			continue
		}
		backoff = 500 * time.Millisecond

		if _, err := fmt.Fprintf(conn, "%s %s %s\n", ProtocolMagic, cfg.NodeID, sessionID); err != nil {
			_ = conn.Close()
			continue
		}
		reader := bufio.NewReader(conn)
		line, err := reader.ReadString('\n')
		if err != nil || strings.TrimSpace(line) != "READY" {
			_ = conn.Close()
			continue
		}
		if hadReadySession {
			logf("RPC relay slot %d restored", slot)
		}
		hadReadySession = true
		if err := serveBridge(ctx, conn, reader); err != nil && ctx.Err() == nil {
			logf("RPC relay slot %d interrupted: %v; reconnecting", slot, err)
		}
		_ = conn.Close()
	}
}

func serveBridge(ctx context.Context, conn net.Conn, reader *bufio.Reader) error {
	for {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		// READY tunnels intentionally have no application-level idle timeout.
		// The Controller periodically validates them using PING/PONG.
		line, err := reader.ReadString('\n')
		if err != nil {
			return err
		}
		fields := strings.Fields(strings.TrimSpace(line))
		if len(fields) == 1 && fields[0] == "PING" {
			if _, err := fmt.Fprintln(conn, "PONG"); err != nil {
				return err
			}
			continue
		}
		if len(fields) != 2 || fields[0] != "BRIDGE" {
			return fmt.Errorf("invalid relay command")
		}
		port, err := strconv.Atoi(fields[1])
		if err != nil || port != DefaultWorkerPort {
			_, _ = fmt.Fprintf(conn, "ERR unsupported typed RPC port\\n")
			return fmt.Errorf("relay command requested unsupported port")
		}

		target := net.JoinHostPort("127.0.0.1", strconv.Itoa(port))
		local, err := net.DialTimeout("tcp", target, 2*time.Second)
		if err != nil {
			_, _ = fmt.Fprintf(conn, "ERR local RPC worker unavailable\\n")
			return err
		}
		defer local.Close()
		if _, err := fmt.Fprintln(conn, "OK"); err != nil {
			return err
		}

		done := make(chan struct{}, 2)
		go func() {
			_, _ = ioCopy(local, reader)
			if c, ok := local.(*net.TCPConn); ok {
				_ = c.CloseWrite()
			}
			done <- struct{}{}
		}()
		go func() {
			_, _ = ioCopy(conn, local)
			if c, ok := conn.(*tls.Conn); ok {
				_ = c.CloseWrite()
			}
			done <- struct{}{}
		}()
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-done:
			return nil
		}
	}
}

func newRelaySessionID() (string, error) {
	var raw [16]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", fmt.Errorf("generate relay session id: %w", err)
	}
	return hex.EncodeToString(raw[:]), nil
}

// ioCopy is a small local wrapper to keep the relay package dependency-free.
func ioCopy(dst net.Conn, src interface{ Read([]byte) (int, error) }) (int64, error) {
	buf := make([]byte, 32*1024)
	var total int64
	for {
		n, rerr := src.Read(buf)
		if n > 0 {
			written := 0
			for written < n {
				m, werr := dst.Write(buf[written:n])
				total += int64(m)
				written += m
				if werr != nil {
					return total, werr
				}
			}
		}
		if rerr != nil {
			return total, rerr
		}
	}
}

func sleepContext(ctx context.Context, d time.Duration) {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
	case <-timer.C:
	}
}
