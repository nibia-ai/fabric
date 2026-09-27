package scheduler

import (
	"testing"
	"time"

	"github.com/nibia-ai/fabric/internal/types"
)

func node(
	id, name string,
	physical, logical int,
	totalMB, availableMB uint64,
	cpuUsed float64,
	osName, arch, resource string,
	caps ...string,
) types.NodeStatus {
	return types.NodeStatus{
		Node: types.Node{
			ID: id, Name: name,
			CPUPhysical: physical, CPULogical: logical,
			MemoryTotalMB: totalMB, MemoryAvailableMB: availableMB,
			CPUUsedPct: cpuUsed, MemoryPressurePct: -1,
			OS: osName, Arch: arch, ResourceState: resource,
			Capabilities: caps,
		},
		LastSeen: time.Now(),
		State:    "online",
	}
}

func TestHeavyCoreJobSelectsMac(t *testing.T) {
	nodes := []types.NodeStatus{
		node("mac", "Mac-M2", 12, 12, 16384, 6500, 15, "darwin", "arm64", "ready", "cpu", "arm64"),
		node("linux", "Linux-Ryzen", 6, 12, 5734, 3300, 1, "linux", "amd64", "ready", "cpu", "amd64"),
	}

	d := Place(nodes, types.JobRequirements{MinPhysicalCores: 8})
	if d.SelectedNodeID != "mac" {
		t.Fatalf("selected=%q want mac", d.SelectedNodeID)
	}
}

func TestOSConstraintSelectsLinux(t *testing.T) {
	nodes := []types.NodeStatus{
		node("mac", "Mac-M2", 12, 12, 16384, 6500, 10, "darwin", "arm64", "ready", "cpu"),
		node("linux", "Linux-Ryzen", 6, 12, 5734, 3300, 1, "linux", "amd64", "ready", "cpu"),
	}

	d := Place(nodes, types.JobRequirements{OS: "linux"})
	if d.SelectedNodeID != "linux" {
		t.Fatalf("selected=%q want linux", d.SelectedNodeID)
	}
}

func TestUnschedulableExplainsRAM(t *testing.T) {
	nodes := []types.NodeStatus{
		node("linux", "Linux-Ryzen", 6, 12, 5734, 3000, 1, "linux", "amd64", "ready", "cpu"),
	}

	d := Place(nodes, types.JobRequirements{MinMemoryMB: 8192})
	if d.SelectedNodeID != "" {
		t.Fatalf("expected no selected node, got %q", d.SelectedNodeID)
	}
	if len(d.Candidates) != 1 || len(d.Candidates[0].RejectionReasons) == 0 {
		t.Fatal("expected a rejection explanation")
	}
}

func TestOfflineNodeNeverWins(t *testing.T) {
	a := node("fast", "Fast", 32, 64, 65536, 60000, 0, "linux", "amd64", "ready", "cpu")
	a.State = "offline"
	b := node("small", "Small", 4, 8, 8192, 6000, 10, "linux", "amd64", "ready", "cpu")

	d := Place([]types.NodeStatus{a, b}, types.JobRequirements{})
	if d.SelectedNodeID != "small" {
		t.Fatalf("selected=%q want small", d.SelectedNodeID)
	}
}

func TestLifecycleCleanupCanRemainPinnedToTemporarilyOfflineNode(t *testing.T) {
	win := node("win", "Windows-Worker", 10, 12, 16000, 1500, 70, "windows", "amd64", "PRESSURED",
		"executor-v1", "task-llamacpp-rpc-worker", "runtime:llama.cpp", "llamacpp:rpc-worker")
	win.State = "offline"

	req := types.JobRequirements{
		RequiredNodeID: "win",
		AllowBusy:      true,
		Capabilities:   []string{"executor-v1", "task-llamacpp-rpc-worker"},
	}
	d := PlaceLifecycleCleanup([]types.NodeStatus{win}, req, types.PlacementHints{})
	if d.SelectedNodeID != "win" {
		t.Fatalf("selected=%q want win; candidates=%+v", d.SelectedNodeID, d.Candidates)
	}
}

func TestPlaceWithHintsUsesSoftAffinity(t *testing.T) {
	nodes := []types.NodeStatus{
		{
			State: "online",
			Node: types.Node{
				ID: "a", Name: "A",
				CPUPhysical: 8, CPULogical: 8,
				MemoryTotalMB: 16000, MemoryAvailableMB: 12000,
				CPUUsedPct: 20, MemoryPressurePct: 80,
				ResourceState: "READY",
				Capabilities:  []string{"model:test"},
			},
		},
		{
			State: "online",
			Node: types.Node{
				ID: "b", Name: "B",
				CPUPhysical: 8, CPULogical: 8,
				MemoryTotalMB: 16000, MemoryAvailableMB: 12000,
				CPUUsedPct: 20, MemoryPressurePct: 80,
				ResourceState: "READY",
				Capabilities:  []string{"model:test"},
			},
		},
	}

	req := types.JobRequirements{Capabilities: []string{"model:test"}}
	hints := types.PlacementHints{
		NodeAffinity: map[string]float64{"a": 0.5, "b": 1.0},
	}
	got := PlaceWithHints(nodes, req, hints)
	if got.SelectedNodeID != "b" {
		t.Fatalf("selected=%q want b", got.SelectedNodeID)
	}
	if len(got.Candidates) != 2 {
		t.Fatalf("candidates=%d", len(got.Candidates))
	}
	if got.Candidates[0].AffinityScore <= got.Candidates[1].AffinityScore {
		t.Fatalf("expected selected candidate to have higher affinity: %+v", got.Candidates)
	}
}

func TestRequiredNodeIDHardPinsPlacement(t *testing.T) {
	nodes := []types.NodeStatus{
		{
			State: "online",
			Node: types.Node{
				ID: "fast", Name: "Fast",
				CPUPhysical: 16, CPULogical: 32,
				MemoryTotalMB: 32000, MemoryAvailableMB: 30000,
				ResourceState: "READY",
			},
		},
		{
			State: "online",
			Node: types.Node{
				ID: "pinned", Name: "Pinned",
				CPUPhysical: 4, CPULogical: 8,
				MemoryTotalMB: 8000, MemoryAvailableMB: 6000,
				ResourceState: "READY",
			},
		},
	}

	got := Place(nodes, types.JobRequirements{RequiredNodeID: "pinned"})
	if got.SelectedNodeID != "pinned" {
		t.Fatalf("selected=%q want pinned", got.SelectedNodeID)
	}
}

func TestExcludedNodeIDRejectsRetryTarget(t *testing.T) {
	nodes := []types.NodeStatus{
		{
			State: "online",
			Node: types.Node{
				ID: "a", Name: "A",
				CPUPhysical: 8, CPULogical: 8,
				MemoryTotalMB: 16000, MemoryAvailableMB: 12000,
				ResourceState: "READY",
			},
		},
		{
			State: "online",
			Node: types.Node{
				ID: "b", Name: "B",
				CPUPhysical: 4, CPULogical: 8,
				MemoryTotalMB: 8000, MemoryAvailableMB: 6000,
				ResourceState: "READY",
			},
		},
	}
	got := Place(nodes, types.JobRequirements{ExcludedNodeIDs: []string{"a"}})
	if got.SelectedNodeID != "b" {
		t.Fatalf("selected=%q want b", got.SelectedNodeID)
	}
}
