package rpcrelay

import (
	"bufio"
	"fmt"
	"io"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/nibia-ai/fabric/internal/types"
)

func TestManagerProbeUsesAuthenticatedReadyTunnel(t *testing.T) {
	hub := NewHub()
	manager := NewManager(hub)

	controllerSide, agentSide := net.Pipe()
	if err := hub.Register("node-a", "session-a", controllerSide); err != nil {
		t.Fatal(err)
	}

	// Probe validates only the authenticated relay control tunnel with
	// PING/PONG. It must not open a raw worker connection.
	go func() {
		defer agentSide.Close()
		reader := bufio.NewReader(agentSide)
		line, err := reader.ReadString('\n')
		if err != nil {
			return
		}
		if strings.TrimSpace(line) != "PING" {
			_, _ = fmt.Fprintln(agentSide, "ERR unexpected command")
			return
		}
		_, _ = fmt.Fprintln(agentSide, "PONG")
	}()

	tmp, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	localPort := tmp.Addr().(*net.TCPAddr).Port
	_ = tmp.Close()

	status, err := manager.Start(types.GenerativeRelayRequest{
		NodeID: "node-a", NodeName: "worker-a",
		LocalPort: localPort, RemotePort: DefaultWorkerPort,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer manager.Stop("node-a")
	if !status.Running || status.LocalEndpoint == "" {
		t.Fatalf("unexpected relay status: %+v", status)
	}

	result := manager.Probe("node-a", 2*time.Second)
	if !result.Success {
		t.Fatalf("probe failed: %+v status=%+v", result, manager.Status("node-a"))
	}
	after := manager.Status("node-a")
	if after.SuccessfulBridges != 0 || after.FailedBridges != 0 {
		t.Fatalf("control-tunnel probe must not affect worker bridge counters: %+v", after)
	}
}

func TestManagerRejectsGenericProxyPort(t *testing.T) {
	manager := NewManager(NewHub())
	_, err := manager.Start(types.GenerativeRelayRequest{
		NodeID: "node-a", LocalPort: 56052, RemotePort: 12345,
	})
	if err == nil || !strings.Contains(err.Error(), "only permits") {
		t.Fatalf("expected typed port rejection, got %v", err)
	}
}

func TestDeriveRelayAddress(t *testing.T) {
	got, err := DeriveRelayAddress("https://192.168.1.20:8443", 9443)
	if err != nil {
		t.Fatal(err)
	}
	if got != "192.168.1.20:9443" {
		t.Fatalf("got %q", got)
	}
}

func TestSessionReplacementDropsOldReadyTunnels(t *testing.T) {
	hub := NewHub()

	oldController, oldAgent := net.Pipe()
	defer oldAgent.Close()
	if err := hub.Register("node-a", "session-old", oldController); err != nil {
		t.Fatal(err)
	}
	if got := hub.ReadyCount("node-a"); got != 1 {
		t.Fatalf("expected 1 old tunnel, got %d", got)
	}

	newController, newAgent := net.Pipe()
	defer newAgent.Close()
	if err := hub.Register("node-a", "session-new", newController); err != nil {
		t.Fatal(err)
	}
	if got := hub.ReadyCount("node-a"); got != 1 {
		t.Fatalf("expected old session to be replaced by one new tunnel, got %d", got)
	}

	_ = oldAgent.SetWriteDeadline(time.Now().Add(100 * time.Millisecond))
	if _, err := oldAgent.Write([]byte("x")); err == nil {
		t.Fatal("expected old tunnel peer to be closed")
	}
}

func TestSweepRemovesDeadTunnelAndKeepsHealthyTunnel(t *testing.T) {
	hub := NewHub()

	deadController, deadAgent := net.Pipe()
	if err := hub.Register("node-a", "session-a", deadController); err != nil {
		t.Fatal(err)
	}
	_ = deadAgent.Close()

	healthyController, healthyAgent := net.Pipe()
	if err := hub.Register("node-a", "session-a", healthyController); err != nil {
		t.Fatal(err)
	}
	go func() {
		defer healthyAgent.Close()
		reader := bufio.NewReader(healthyAgent)
		line, err := reader.ReadString('\n')
		if err == nil && strings.TrimSpace(line) == "PING" {
			_, _ = fmt.Fprintln(healthyAgent, "PONG")
		}
	}()

	hub.sweepNode("node-a", 200*time.Millisecond)
	if got := hub.ReadyCount("node-a"); got != 1 {
		t.Fatalf("expected only healthy tunnel to remain, got %d", got)
	}
}

func TestProbeReadyTunnelRetriesPastStaleConnections(t *testing.T) {
	hub := NewHub()
	manager := NewManager(hub)

	// Two stale tunnels first.
	for i := 0; i < 2; i++ {
		controllerSide, agentSide := net.Pipe()
		if err := hub.Register("node-a", "session-a", controllerSide); err != nil {
			t.Fatal(err)
		}
		_ = agentSide.Close()
	}

	// Then one healthy authenticated tunnel.
	controllerSide, agentSide := net.Pipe()
	if err := hub.Register("node-a", "session-a", controllerSide); err != nil {
		t.Fatal(err)
	}
	go func() {
		defer agentSide.Close()
		reader := bufio.NewReader(agentSide)
		line, err := reader.ReadString('\n')
		if err == nil && strings.TrimSpace(line) == "PING" {
			_, _ = fmt.Fprintln(agentSide, "PONG")
		}
	}()

	tmp, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	localPort := tmp.Addr().(*net.TCPAddr).Port
	_ = tmp.Close()

	_, err = manager.Start(types.GenerativeRelayRequest{
		NodeID: "node-a", NodeName: "worker-a",
		LocalPort: localPort, RemotePort: DefaultWorkerPort,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer manager.Stop("node-a")

	result := manager.Probe("node-a", 2*time.Second)
	if !result.Success {
		t.Fatalf("expected stale tunnels to be skipped: %+v", result)
	}
	if got := hub.ReadyCount("node-a"); got != 1 {
		t.Fatalf("expected one healthy ready tunnel to remain, got %d", got)
	}
}

func TestBridgeConnsCountsBytes(t *testing.T) {
	state := &relayState{}
	coordinatorSide, clientSide := net.Pipe()
	workerSide, remoteSide := net.Pipe()

	done := make(chan struct{})
	go func() {
		bridgeConns(state, coordinatorSide, workerSide)
		close(done)
	}()

	toWorker := []byte("hello-worker")
	go func() {
		_, _ = clientSide.Write(toWorker)
	}()
	buf := make([]byte, len(toWorker))
	if _, err := io.ReadFull(remoteSide, buf); err != nil {
		t.Fatal(err)
	}
	if string(buf) != string(toWorker) {
		t.Fatalf("worker received %q", string(buf))
	}

	fromWorker := []byte("hello-coordinator")
	go func() {
		_, _ = remoteSide.Write(fromWorker)
	}()
	buf = make([]byte, len(fromWorker))
	if _, err := io.ReadFull(clientSide, buf); err != nil {
		t.Fatal(err)
	}
	if string(buf) != string(fromWorker) {
		t.Fatalf("coordinator received %q", string(buf))
	}

	_ = clientSide.Close()
	_ = remoteSide.Close()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("bridge did not stop")
	}

	if got := state.toWorker.Load(); got != uint64(len(toWorker)) {
		t.Fatalf("bytes to worker=%d want=%d", got, len(toWorker))
	}
	if got := state.fromWorker.Load(); got != uint64(len(fromWorker)) {
		t.Fatalf("bytes from worker=%d want=%d", got, len(fromWorker))
	}
}
