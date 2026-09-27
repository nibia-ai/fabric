package main

import (
	"bufio"
	"fmt"
	"net"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"
)

const serveInstanceGuardAddress = "127.0.0.1:55051"

type serveInstanceGuard struct {
	listener net.Listener
	stop     chan struct{}
	once     sync.Once
	info     string
}

// acquireServeInstanceGuard uses a Primary-local TCP listener as a lightweight,
// cross-platform mutex. The OS owns lifecycle cleanup, so crashes and os.Exit do
// not leave stale lock files. 55051 is reserved by NIBIA immediately before the
// loopback relay pool that starts at 55052.
func acquireServeInstanceGuard(host string, port int, model string) (*serveInstanceGuard, error) {
	ln, err := net.Listen("tcp", serveInstanceGuardAddress)
	if err != nil {
		detail := queryServeInstanceGuard()
		if detail == "" {
			return nil, fmt.Errorf("another NIBIA SERVE may already be active on this Primary (instance guard %s is busy)", serveInstanceGuardAddress)
		}
		return nil, fmt.Errorf("NIBIA SERVE is already running on this Primary: %s", detail)
	}
	uiHost := displayServeHost(host)
	info := fmt.Sprintf("PID %d, Web UI http://%s:%d/, model %s", os.Getpid(), uiHost, port, cleanModelDisplayName(model))
	g := &serveInstanceGuard{listener: ln, stop: make(chan struct{}), info: info}
	go g.serve()
	return g, nil
}

func (g *serveInstanceGuard) serve() {
	for {
		if tcp, ok := g.listener.(*net.TCPListener); ok {
			_ = tcp.SetDeadline(time.Now().Add(time.Second))
		}
		conn, err := g.listener.Accept()
		if err != nil {
			select {
			case <-g.stop:
				return
			default:
				if ne, ok := err.(net.Error); ok && ne.Timeout() {
					continue
				}
				return
			}
		}
		_, _ = fmt.Fprintln(conn, "NIBIA_SERVE_V1\t"+g.info)
		_ = conn.Close()
	}
}

func queryServeInstanceGuard() string {
	conn, err := net.DialTimeout("tcp", serveInstanceGuardAddress, 250*time.Millisecond)
	if err != nil {
		return ""
	}
	defer conn.Close()
	_ = conn.SetReadDeadline(time.Now().Add(250 * time.Millisecond))
	line, err := bufio.NewReader(conn).ReadString('\n')
	if err != nil {
		return ""
	}
	line = strings.TrimSpace(line)
	const prefix = "NIBIA_SERVE_V1\t"
	if !strings.HasPrefix(line, prefix) {
		return ""
	}
	return strings.TrimPrefix(line, prefix)
}

func (g *serveInstanceGuard) Close() {
	if g == nil {
		return
	}
	g.once.Do(func() {
		close(g.stop)
		_ = g.listener.Close()
	})
}

func serveGuardPort() int {
	_, p, _ := net.SplitHostPort(serveInstanceGuardAddress)
	n, _ := strconv.Atoi(p)
	return n
}
