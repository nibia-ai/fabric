package main

import (
	"context"
	"github.com/nibia-ai/fabric/internal/sysinfo"
	"log"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/nibia-ai/fabric/internal/types"
)

func TestHeartbeatContinuesWhileTelemetryCollectionIsBlocked(t *testing.T) {
	var heartbeats atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/nodes" {
			http.NotFound(w, r)
			return
		}
		heartbeats.Add(1)
		w.WriteHeader(http.StatusAccepted)
	}))
	defer server.Close()

	col := &collector{}
	col.storeSnapshot(types.Node{
		ID:                "node-test",
		Name:              "test-node",
		CPUUsedPct:        10,
		MemoryAvailableMB: 4096,
		ResourceState:     "ready",
		ResourceScore:     0.8,
		UpdatedAt:         time.Now().UTC(),
		AgentVersion:      "test",
		ProtocolVersion:   "2",
	})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	blocked := make(chan struct{})
	go telemetryLoop(ctx, col, 10*time.Millisecond, func() types.Node {
		<-blocked
		return types.Node{ID: "node-test", UpdatedAt: time.Now().UTC()}
	})
	go heartbeatLoop(ctx, server.Client(), server.URL, col, 25*time.Millisecond)

	deadline := time.Now().Add(350 * time.Millisecond)
	for time.Now().Before(deadline) {
		if heartbeats.Load() >= 3 {
			cancel()
			close(blocked)
			return
		}
		time.Sleep(10 * time.Millisecond)
	}

	cancel()
	close(blocked)
	t.Fatalf("expected heartbeats to continue from cached telemetry; got %d", heartbeats.Load())
}

type blockingLogWriter struct {
	gate <-chan struct{}
}

func (w blockingLogWriter) Write(p []byte) (int, error) {
	<-w.gate
	return len(p), nil
}

func TestHeartbeatContinuesWhenConsoleLoggingBlocks(t *testing.T) {
	var heartbeats atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/nodes" {
			http.NotFound(w, r)
			return
		}
		heartbeats.Add(1)
		w.WriteHeader(http.StatusAccepted)
	}))
	defer server.Close()

	col := &collector{}
	col.storeSnapshot(types.Node{
		ID:                "node-test",
		Name:              "test-node",
		CPUUsedPct:        10,
		MemoryAvailableMB: 4096,
		ResourceState:     "busy",
		ResourceScore:     0.5,
		UpdatedAt:         time.Now().UTC(),
		AgentVersion:      "test",
		ProtocolVersion:   "2",
	})

	oldOutput := log.Writer()
	gate := make(chan struct{})
	log.SetOutput(blockingLogWriter{gate: gate})

	ctx, cancel := context.WithCancel(context.Background())
	go heartbeatLoop(ctx, server.Client(), server.URL, col, 25*time.Millisecond)

	deadline := time.Now().Add(350 * time.Millisecond)
	for time.Now().Before(deadline) {
		if heartbeats.Load() >= 3 {
			cancel()
			close(gate)
			time.Sleep(20 * time.Millisecond)
			log.SetOutput(oldOutput)
			return
		}
		time.Sleep(10 * time.Millisecond)
	}

	cancel()
	close(gate)
	time.Sleep(20 * time.Millisecond)
	log.SetOutput(oldOutput)
	t.Fatalf("expected heartbeat POSTs to continue while the log writer is blocked; got %d", heartbeats.Load())
}

func TestMergeStaticInfoKeepsKnownFieldsAndAcceptsRicherRetry(t *testing.T) {
	old := sysinfo.StaticInfo{Platform: "Windows", CPULogical: 12, CPUPhysical: 12}
	fresh := sysinfo.StaticInfo{Platform: "Windows", CPUModel: "Test CPU", CPUPhysical: 10, CPULogical: 12}
	got := mergeStaticInfo(old, fresh)
	if got.Platform != "Windows" || got.CPUModel != "Test CPU" || got.CPUPhysical != 10 || got.CPULogical != 12 {
		t.Fatalf("unexpected merged static info: %+v", got)
	}
}
