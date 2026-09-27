package lease

import (
	"testing"
	"time"

	"github.com/nibia-ai/fabric/internal/types"
)

func placedJob(id, nodeID, nodeName string) types.ScheduledJob {
	now := time.Now().UTC()
	return types.ScheduledJob{
		ID:           id,
		Name:         "test",
		State:        JobPlaced,
		Requirements: types.JobRequirements{},
		Workload:     types.WorkloadSpec{Type: "llamacpp.rpc.status"},
		Placement: types.PlacementDecision{
			SelectedNodeID:   nodeID,
			SelectedNodeName: nodeName,
		},
		MaxAttempts:     3,
		CreatedAt:       now,
		UpdatedAt:       now,
		ExecutionStatus: "QUEUED",
	}
}

func TestClaimAckRenewComplete(t *testing.T) {
	now := time.Now().UTC()
	store := NewStore([]types.ScheduledJob{placedJob("job-1", "node-a", "Node A")}, nil)
	ttl := 15 * time.Second

	a, ok, err := store.ClaimNext("node-a", ttl, now)
	if err != nil || !ok {
		t.Fatalf("claim ok=%v err=%v", ok, err)
	}
	if a.JobID != "job-1" || a.LeaseID == "" {
		t.Fatalf("bad assignment: %+v", a)
	}

	if _, err := store.Ack(a.LeaseID, "node-a", ttl, now.Add(time.Second)); err != nil {
		t.Fatal(err)
	}

	if _, err := store.Renew(a.LeaseID, "node-a", ttl, now.Add(5*time.Second)); err != nil {
		t.Fatal(err)
	}

	executionStart := now.Add(2 * time.Second)
	job, err := store.Complete(
		a.LeaseID, "node-a", "Node A", true, "ok", 42,
		&executionStart, nil, now.Add(6*time.Second),
	)
	if err != nil {
		t.Fatal(err)
	}
	if job.State != JobSucceeded || job.Result == nil || !job.Result.Success {
		t.Fatalf("unexpected terminal job: %+v", job)
	}
	if job.Result.ExecutionStartedAt == nil ||
		!job.Result.ExecutionStartedAt.Equal(executionStart) {
		t.Fatalf("execution start missing: %+v", job.Result)
	}
	if len(job.Attempts) != 1 || job.Attempts[0].Status != LeaseCompleted {
		t.Fatalf("attempt history not completed: %+v", job.Attempts)
	}
}

func TestExpiredLeaseRetriesOnDifferentNode(t *testing.T) {
	now := time.Now().UTC()
	store := NewStore([]types.ScheduledJob{placedJob("job-1", "node-a", "Node A")}, nil)
	ttl := 10 * time.Second

	a, ok, err := store.ClaimNext("node-a", ttl, now)
	if err != nil || !ok {
		t.Fatalf("claim ok=%v err=%v", ok, err)
	}
	if _, err := store.Ack(a.LeaseID, "node-a", ttl, now.Add(time.Second)); err != nil {
		t.Fatal(err)
	}

	nodes := []types.NodeStatus{
		{
			Node: types.Node{
				ID: "node-a", Name: "Node A",
				CPUPhysical: 8, CPULogical: 8,
				MemoryTotalMB: 8192, MemoryAvailableMB: 6000,
				CPUUsedPct: 2, MemoryPressurePct: -1,
				ResourceState: "ready", Capabilities: []string{"cpu", "lease-v1"},
			},
			State: "online",
		},
		{
			Node: types.Node{
				ID: "node-b", Name: "Node B",
				CPUPhysical: 4, CPULogical: 8,
				MemoryTotalMB: 8192, MemoryAvailableMB: 6000,
				CPUUsedPct: 2, MemoryPressurePct: -1,
				ResourceState: "ready", Capabilities: []string{"cpu", "lease-v1"},
			},
			State: "online",
		},
	}

	changed, err := store.ReapExpired(nodes, ttl, now.Add(20*time.Second))
	if err != nil {
		t.Fatal(err)
	}
	if changed != 1 {
		t.Fatalf("changed=%d want 1", changed)
	}

	job, _ := store.Get("job-1")
	if job.State != JobPlaced {
		t.Fatalf("state=%s want PLACED", job.State)
	}
	if job.Placement.SelectedNodeID != "node-b" {
		t.Fatalf("selected=%s want node-b", job.Placement.SelectedNodeID)
	}
}

func TestCancellationSignalsRunningLease(t *testing.T) {
	now := time.Now().UTC()
	store := NewStore([]types.ScheduledJob{placedJob("job-1", "node-a", "Node A")}, nil)
	ttl := 15 * time.Second

	a, ok, err := store.ClaimNext("node-a", ttl, now)
	if err != nil || !ok {
		t.Fatalf("claim ok=%v err=%v", ok, err)
	}
	if _, err := store.Ack(a.LeaseID, "node-a", ttl, now.Add(time.Second)); err != nil {
		t.Fatal(err)
	}

	job, err := store.Cancel("job-1", now.Add(2*time.Second))
	if err != nil {
		t.Fatal(err)
	}
	if job.State != JobCancelled || job.ActiveLease == nil || !job.ActiveLease.CancelRequested {
		t.Fatalf("cancel did not mark active lease: %+v", job)
	}

	resp, err := store.Renew(a.LeaseID, "node-a", ttl, now.Add(3*time.Second))
	if err != ErrCancelled {
		t.Fatalf("renew err=%v want ErrCancelled", err)
	}
	if !resp.CancelRequested {
		t.Fatal("renew did not report cancellation")
	}
}
