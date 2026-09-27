package fabric

import (
	"testing"
	"time"

	"github.com/nibia-ai/fabric/internal/types"
)

func TestGenerativeSummaryRequiresDistinctCoordinatorAndWorker(t *testing.T) {
	nodes := []types.NodeStatus{
		{State: "ONLINE", Node: types.Node{
			ID: "mac", Name: "Mac", MemoryTotalMB: 16384, MemoryAvailableMB: 9000,
			Runtimes: []types.RuntimeInventory{{
				Name: "llama.cpp", Installed: true, Reachable: true,
				Features: []string{"cli", "rpc-coordinator"},
			}},
		}},
		{State: "ONLINE", Node: types.Node{
			ID: "linux", Name: "Linux", MemoryTotalMB: 6144, MemoryAvailableMB: 4000,
			Runtimes: []types.RuntimeInventory{{
				Name: "llama.cpp", Installed: true, Reachable: true,
				Features: []string{"rpc-worker"},
			}},
		}},
	}
	got := GenerativeSummary(nodes)
	if !got.DistributedGenerativeReady {
		t.Fatal("expected distributed generative topology to be ready")
	}
	if got.DistributedCoordinatorNodes != 1 || got.DistributedWorkerNodes != 1 {
		t.Fatalf("unexpected coordinator/worker counts: %+v", got)
	}
	if got.AggregateTotalMemoryMB != 22528 || got.AggregateAvailableMemoryMB != 13000 {
		t.Fatalf("unexpected aggregate memory: %+v", got)
	}
	if got.SharedAddressSpace {
		t.Fatal("NIBIA must never report a shared address space")
	}
}

func TestGenerativeSummarySameNodeOnlyIsNotDistributedReady(t *testing.T) {
	nodes := []types.NodeStatus{{State: "ONLINE", Node: types.Node{
		ID: "mac", Name: "Mac",
		Runtimes: []types.RuntimeInventory{{
			Name: "llama.cpp", Installed: true,
			Features: []string{"rpc-coordinator", "rpc-worker"},
		}},
	}}}
	got := GenerativeSummary(nodes)
	if got.DistributedGenerativeReady {
		t.Fatal("single-node topology is not distributed generative ready")
	}
}

func TestGenerativeSummaryCarriesProcessScopedRPCWorkerTelemetry(t *testing.T) {
	started := time.Now().UTC().Add(-time.Minute)
	nodes := []types.NodeStatus{{State: "ONLINE", Node: types.Node{
		ID: "linux", Name: "Linux", MemoryTotalMB: 6144, MemoryAvailableMB: 3000,
		RPCWorkerManaged: true, RPCWorkerRunning: true, RPCWorkerPID: 123,
		RPCWorkerProcessCPUPercent: 12.5, RPCWorkerProcessRSSBytes: 64 << 20,
		RPCWorkerPeakCPUPercent: 598.9, RPCWorkerPeakRSSBytes: 3792 << 20,
		RPCWorkerTelemetryStartedAt: started,
		Runtimes: []types.RuntimeInventory{{
			Name: "llama.cpp", Installed: true, Reachable: true, Features: []string{"rpc-worker"},
		}},
	}}}
	got := GenerativeSummary(nodes)
	if len(got.Nodes) != 1 {
		t.Fatalf("expected one generative node, got %d", len(got.Nodes))
	}
	n := got.Nodes[0]
	if !n.RPCWorkerRunning || n.RPCWorkerPID != 123 || n.RPCWorkerPeakCPUPercent != 598.9 || n.RPCWorkerPeakRSSBytes != 3792<<20 {
		t.Fatalf("worker telemetry not preserved: %+v", n)
	}
	if !n.RPCWorkerTelemetryStartedAt.Equal(started) {
		t.Fatalf("unexpected telemetry start %s", n.RPCWorkerTelemetryStartedAt)
	}
}
