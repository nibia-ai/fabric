package rpcrelay

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"net"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/nibia-ai/fabric/internal/types"
)

const (
	ProtocolMagic         = "NIBIA-RPC-RELAY/1"
	DefaultWorkerPort     = 50052
	DefaultControllerPort = 9443
	DefaultLocalRelayPort = 55052
)

type readyTunnel struct {
	conn      net.Conn
	sessionID string
}

type Hub struct {
	mu      sync.Mutex
	ready   map[string]chan readyTunnel
	session map[string]string
}

func NewHub() *Hub {
	return &Hub{
		ready:   map[string]chan readyTunnel{},
		session: map[string]string{},
	}
}

func (h *Hub) queueLocked(nodeID string) chan readyTunnel {
	q := h.ready[nodeID]
	if q == nil {
		q = make(chan readyTunnel, 16)
		h.ready[nodeID] = q
	}
	return q
}

func (h *Hub) queue(nodeID string) chan readyTunnel {
	nodeID = strings.TrimSpace(nodeID)
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.queueLocked(nodeID)
}

// Register groups ready tunnels by Agent process session. When a restarted
// Agent presents a new session for the same node identity, queued tunnels from
// the previous process are closed and discarded immediately.
func (h *Hub) Register(nodeID, sessionID string, conn net.Conn) error {
	if conn == nil {
		return errors.New("relay connection is nil")
	}
	nodeID = strings.TrimSpace(nodeID)
	sessionID = strings.TrimSpace(sessionID)
	if nodeID == "" || sessionID == "" {
		_ = conn.Close()
		return errors.New("relay node id and session id are required")
	}

	h.mu.Lock()
	q := h.queueLocked(nodeID)
	if previous := h.session[nodeID]; previous != "" && previous != sessionID {
		for {
			select {
			case stale := <-q:
				_ = stale.conn.Close()
			default:
				h.session[nodeID] = sessionID
				h.mu.Unlock()
				goto enqueue
			}
		}
	}
	h.session[nodeID] = sessionID
	h.mu.Unlock()

enqueue:
	select {
	case q <- readyTunnel{conn: conn, sessionID: sessionID}:
		return nil
	default:
		_ = conn.Close()
		return fmt.Errorf("relay ready pool is full for node %s", nodeID)
	}
}

func (h *Hub) Acquire(nodeID string, timeout time.Duration) (net.Conn, error) {
	nodeID = strings.TrimSpace(nodeID)
	if nodeID == "" {
		return nil, errors.New("relay node id is required")
	}
	if timeout <= 0 {
		timeout = 5 * time.Second
	}
	q := h.queue(nodeID)
	deadline := time.Now().Add(timeout)
	for {
		remaining := time.Until(deadline)
		if remaining <= 0 {
			return nil, fmt.Errorf("no authenticated relay tunnel ready for node %s", nodeID)
		}
		timer := time.NewTimer(remaining)
		select {
		case tunnel := <-q:
			if !timer.Stop() {
				<-timer.C
			}
			h.mu.Lock()
			currentSession := h.session[nodeID]
			h.mu.Unlock()
			if tunnel.sessionID != currentSession {
				_ = tunnel.conn.Close()
				continue
			}
			return tunnel.conn, nil
		case <-timer.C:
			return nil, fmt.Errorf("no authenticated relay tunnel ready for node %s", nodeID)
		}
	}
}

// ProbeReadyTunnel validates the authenticated Agent<->Controller relay
// control channel without opening a raw TCP connection to ggml-rpc-server.
// This deliberately avoids zero-byte connects to the RPC worker, which are not
// a protocol-safe health check for llama.cpp RPC.
func (h *Hub) ProbeReadyTunnel(nodeID string, timeout time.Duration) error {
	nodeID = strings.TrimSpace(nodeID)
	if nodeID == "" {
		return errors.New("relay node id is required")
	}
	if timeout <= 0 {
		timeout = 2 * time.Second
	}
	deadline := time.Now().Add(timeout)
	q := h.queue(nodeID)
	for {
		remaining := time.Until(deadline)
		if remaining <= 0 {
			return fmt.Errorf("no healthy authenticated relay tunnel ready for node %s", nodeID)
		}
		timer := time.NewTimer(remaining)
		var tunnel readyTunnel
		select {
		case tunnel = <-q:
			if !timer.Stop() {
				<-timer.C
			}
		case <-timer.C:
			return fmt.Errorf("no authenticated relay tunnel ready for node %s", nodeID)
		}

		h.mu.Lock()
		currentSession := h.session[nodeID]
		h.mu.Unlock()
		if tunnel.sessionID != currentSession {
			_ = tunnel.conn.Close()
			continue
		}
		probeTimeout := remaining
		if probeTimeout > 2*time.Second {
			probeTimeout = 2 * time.Second
		}
		if err := pingTunnel(tunnel.conn, probeTimeout); err != nil {
			_ = tunnel.conn.Close()
			continue
		}

		h.mu.Lock()
		stillCurrent := h.session[nodeID] == tunnel.sessionID
		h.mu.Unlock()
		if !stillCurrent {
			_ = tunnel.conn.Close()
			continue
		}
		select {
		case q <- tunnel:
			return nil
		default:
			_ = tunnel.conn.Close()
			return fmt.Errorf("relay ready pool is full for node %s", nodeID)
		}
	}
}

func (h *Hub) ReadyCount(nodeID string) int {
	nodeID = strings.TrimSpace(nodeID)
	h.mu.Lock()
	defer h.mu.Unlock()
	q := h.ready[nodeID]
	if q == nil {
		return 0
	}
	return len(q)
}

// SweepAll verifies idle ready tunnels using the relay control protocol.
// Dead sockets are closed and removed instead of remaining counted as ready.
func (h *Hub) SweepAll(timeout time.Duration) {
	h.mu.Lock()
	nodeIDs := make([]string, 0, len(h.ready))
	for nodeID := range h.ready {
		nodeIDs = append(nodeIDs, nodeID)
	}
	h.mu.Unlock()
	for _, nodeID := range nodeIDs {
		h.sweepNode(nodeID, timeout)
	}
}

func (h *Hub) sweepNode(nodeID string, timeout time.Duration) {
	if timeout <= 0 {
		timeout = 2 * time.Second
	}
	q := h.queue(nodeID)
	n := len(q)
	for i := 0; i < n; i++ {
		var tunnel readyTunnel
		select {
		case tunnel = <-q:
		default:
			return
		}
		h.mu.Lock()
		currentSession := h.session[nodeID]
		h.mu.Unlock()
		if tunnel.sessionID != currentSession {
			_ = tunnel.conn.Close()
			continue
		}
		if err := pingTunnel(tunnel.conn, timeout); err != nil {
			_ = tunnel.conn.Close()
			continue
		}
		h.mu.Lock()
		stillCurrent := h.session[nodeID] == tunnel.sessionID
		h.mu.Unlock()
		if !stillCurrent {
			_ = tunnel.conn.Close()
			continue
		}
		select {
		case q <- tunnel:
		default:
			_ = tunnel.conn.Close()
		}
	}
}

func pingTunnel(conn net.Conn, timeout time.Duration) error {
	if conn == nil {
		return errors.New("nil relay tunnel")
	}
	_ = conn.SetDeadline(time.Now().Add(timeout))
	defer conn.SetDeadline(time.Time{})
	if _, err := fmt.Fprintln(conn, "PING"); err != nil {
		return err
	}
	line, err := bufio.NewReader(conn).ReadString('\n')
	if err != nil {
		return err
	}
	if strings.TrimSpace(line) != "PONG" {
		return fmt.Errorf("unexpected relay health response %q", strings.TrimSpace(line))
	}
	return nil
}

type relayState struct {
	nodeID     string
	nodeName   string
	localPort  int
	remotePort int
	listener   net.Listener
	startedAt  time.Time
	lastError  string
	accepted   atomic.Uint64
	successful atomic.Uint64
	failed     atomic.Uint64
	active     atomic.Int64
	toWorker   atomic.Uint64
	fromWorker atomic.Uint64
}

type Manager struct {
	mu     sync.RWMutex
	hub    *Hub
	relays map[string]*relayState
}

func NewManager(hub *Hub) *Manager {
	if hub == nil {
		hub = NewHub()
	}
	return &Manager{hub: hub, relays: map[string]*relayState{}}
}

func normalizeLocalPort(port int) (int, error) {
	if port == 0 {
		port = DefaultLocalRelayPort
	}
	if port < 1024 || port > 65535 {
		return 0, fmt.Errorf("local relay port must be between 1024 and 65535")
	}
	return port, nil
}

func normalizeRemotePort(port int) (int, error) {
	if port == 0 {
		port = DefaultWorkerPort
	}
	// The relay is intentionally not a generic localhost proxy. It may bridge
	// only the managed llama.cpp RPC worker endpoint.
	if port != DefaultWorkerPort {
		return 0, fmt.Errorf("relay only permits the managed llama.cpp RPC port %d", DefaultWorkerPort)
	}
	return port, nil
}

func (m *Manager) Start(req types.GenerativeRelayRequest) (types.GenerativeRelayStatus, error) {
	req.NodeID = strings.TrimSpace(req.NodeID)
	req.NodeName = strings.TrimSpace(req.NodeName)
	if req.NodeID == "" {
		return types.GenerativeRelayStatus{}, errors.New("node_id is required")
	}
	localPort, err := normalizeLocalPort(req.LocalPort)
	if err != nil {
		return types.GenerativeRelayStatus{}, err
	}
	remotePort, err := normalizeRemotePort(req.RemotePort)
	if err != nil {
		return types.GenerativeRelayStatus{}, err
	}

	m.mu.Lock()
	if existing := m.relays[req.NodeID]; existing != nil {
		status := m.statusLocked(existing)
		m.mu.Unlock()
		if existing.localPort != localPort || existing.remotePort != remotePort {
			return status, fmt.Errorf("relay for node %s is already running on %s", req.NodeID, status.LocalEndpoint)
		}
		return status, nil
	}

	ln, err := net.Listen("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(localPort)))
	if err != nil {
		m.mu.Unlock()
		return types.GenerativeRelayStatus{}, fmt.Errorf("listen on controller loopback relay: %w", err)
	}
	state := &relayState{
		nodeID:     req.NodeID,
		nodeName:   req.NodeName,
		localPort:  localPort,
		remotePort: remotePort,
		listener:   ln,
		startedAt:  time.Now().UTC(),
	}
	m.relays[req.NodeID] = state
	status := m.statusLocked(state)
	m.mu.Unlock()

	go m.acceptLoop(state)
	return status, nil
}

func (m *Manager) acceptLoop(state *relayState) {
	for {
		localConn, err := state.listener.Accept()
		if err != nil {
			m.mu.RLock()
			current := m.relays[state.nodeID]
			m.mu.RUnlock()
			if current == state {
				m.setError(state, err.Error())
			}
			return
		}
		state.accepted.Add(1)
		go m.bridge(state, localConn)
	}
}

func (m *Manager) bridge(state *relayState, localConn net.Conn) {
	defer localConn.Close()

	deadline := time.Now().Add(7 * time.Second)
	var lastErr error
	for attempts := 0; attempts < 16 && time.Now().Before(deadline); attempts++ {
		remaining := time.Until(deadline)
		if remaining <= 0 {
			break
		}
		tunnel, err := m.hub.Acquire(state.nodeID, remaining)
		if err != nil {
			lastErr = err
			break
		}
		if err := prepareTunnel(tunnel, state.remotePort); err != nil {
			lastErr = err
			_ = tunnel.Close()
			continue
		}
		state.successful.Add(1)
		state.active.Add(1)
		m.setError(state, "")
		bridgeConns(state, localConn, tunnel)
		state.active.Add(-1)
		return
	}
	state.failed.Add(1)
	if lastErr != nil {
		m.setError(state, lastErr.Error())
	}
}

func prepareTunnel(conn net.Conn, remotePort int) error {
	_ = conn.SetDeadline(time.Now().Add(5 * time.Second))
	if _, err := fmt.Fprintf(conn, "BRIDGE %d\n", remotePort); err != nil {
		return fmt.Errorf("send bridge request: %w", err)
	}
	line, err := bufio.NewReader(conn).ReadString('\n')
	if err != nil {
		return fmt.Errorf("read bridge response: %w", err)
	}
	line = strings.TrimSpace(line)
	if line != "OK" {
		if strings.HasPrefix(line, "ERR ") {
			return errors.New(strings.TrimSpace(strings.TrimPrefix(line, "ERR ")))
		}
		return fmt.Errorf("unexpected bridge response %q", line)
	}
	_ = conn.SetDeadline(time.Time{})
	return nil
}

type countingReader struct {
	r       io.Reader
	counter *atomic.Uint64
}

func (r countingReader) Read(p []byte) (int, error) {
	n, err := r.r.Read(p)
	if n > 0 && r.counter != nil {
		r.counter.Add(uint64(n))
	}
	return n, err
}

func bridgeConns(state *relayState, coordinator, worker net.Conn) {
	defer worker.Close()
	done := make(chan struct{}, 2)
	go func() {
		_, _ = io.Copy(coordinator, countingReader{r: worker, counter: &state.fromWorker})
		if c, ok := coordinator.(*net.TCPConn); ok {
			_ = c.CloseWrite()
		}
		done <- struct{}{}
	}()
	go func() {
		_, _ = io.Copy(worker, countingReader{r: coordinator, counter: &state.toWorker})
		if c, ok := worker.(*net.TCPConn); ok {
			_ = c.CloseWrite()
		}
		done <- struct{}{}
	}()
	<-done
}

func (m *Manager) setError(state *relayState, value string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.relays[state.nodeID] == state {
		state.lastError = value
	}
}

func (m *Manager) Probe(nodeID string, timeout time.Duration) types.GenerativeRelayProbeResponse {
	status := m.Status(nodeID)
	out := types.GenerativeRelayProbeResponse{
		NodeID: nodeID, NodeName: status.NodeName, LocalEndpoint: status.LocalEndpoint,
	}
	if !status.Running || status.LocalEndpoint == "" {
		out.Error = "relay is not running"
		return out
	}
	start := time.Now()
	if err := m.hub.ProbeReadyTunnel(nodeID, timeout); err != nil {
		out.Error = err.Error()
		out.LatencyMS = time.Since(start).Milliseconds()
		return out
	}
	out.Success = true
	out.LatencyMS = time.Since(start).Milliseconds()
	return out
}

func (m *Manager) Stop(nodeID string) (types.GenerativeRelayStatus, error) {
	nodeID = strings.TrimSpace(nodeID)
	m.mu.Lock()
	state := m.relays[nodeID]
	if state == nil {
		m.mu.Unlock()
		return types.GenerativeRelayStatus{
			NodeID: nodeID, Managed: true, Running: false,
			Transport: "mTLS reverse relay", Exposure: "controller loopback only",
		}, nil
	}
	delete(m.relays, nodeID)
	_ = state.listener.Close()
	status := m.statusLocked(state)
	status.Running = false
	m.mu.Unlock()
	return status, nil
}

func (m *Manager) Status(nodeID string) types.GenerativeRelayStatus {
	nodeID = strings.TrimSpace(nodeID)
	m.mu.RLock()
	defer m.mu.RUnlock()
	state := m.relays[nodeID]
	if state == nil {
		return types.GenerativeRelayStatus{
			NodeID: nodeID, Managed: true, Running: false,
			Transport: "mTLS reverse relay", Exposure: "controller loopback only",
			ReadyTunnelCount: m.hub.ReadyCount(nodeID),
		}
	}
	return m.statusLocked(state)
}

func (m *Manager) List() []types.GenerativeRelayStatus {
	m.mu.RLock()
	defer m.mu.RUnlock()
	out := make([]types.GenerativeRelayStatus, 0, len(m.relays))
	for _, state := range m.relays {
		out = append(out, m.statusLocked(state))
	}
	return out
}

func (m *Manager) statusLocked(state *relayState) types.GenerativeRelayStatus {
	return types.GenerativeRelayStatus{
		NodeID:              state.nodeID,
		NodeName:            state.nodeName,
		Managed:             true,
		Running:             true,
		LocalEndpoint:       net.JoinHostPort("127.0.0.1", strconv.Itoa(state.localPort)),
		RemoteEndpoint:      net.JoinHostPort("127.0.0.1", strconv.Itoa(state.remotePort)),
		ReadyTunnelCount:    m.hub.ReadyCount(state.nodeID),
		AcceptedConnections: state.accepted.Load(),
		SuccessfulBridges:   state.successful.Load(),
		FailedBridges:       state.failed.Load(),
		ActiveBridges:       state.active.Load(),
		BytesToWorker:       state.toWorker.Load(),
		BytesFromWorker:     state.fromWorker.Load(),
		StartedAt:           state.startedAt,
		LastError:           state.lastError,
		Transport:           "mTLS reverse relay",
		Exposure:            "controller loopback only",
	}
}
