package main

import (
	"strings"
	"testing"
	"time"

	"github.com/nibia-ai/fabric/internal/types"
)

func TestServeRecoverySnapshotFromSummaryPreservesSelectedOrder(t *testing.T) {
	now := time.Now().UTC()
	selected := []types.GenerativeNodeCapability{
		{NodeID: "node-win", NodeName: "Worker-Windows"},
		{NodeID: "node-linux", NodeName: "Worker-Linux"},
	}
	summary := types.GenerativeFabricSummary{Nodes: []types.GenerativeNodeCapability{
		{NodeID: "node-linux", NodeName: "Worker-Linux", NodeState: "READY", LastSeen: now, TelemetryUpdatedAt: now},
		{NodeID: "node-win", NodeName: "Worker-Windows", NodeState: "READY", LastSeen: now, TelemetryUpdatedAt: now},
	}}

	got := serveRecoverySnapshotFromSummary(summary, selected, now)
	if len(got.Issues) != 0 {
		t.Fatalf("unexpected recovery issues: %v", got.Issues)
	}
	if len(got.Nodes) != 2 {
		t.Fatalf("got %d recovered nodes, want 2", len(got.Nodes))
	}
	if got.Nodes[0].NodeID != "node-win" || got.Nodes[1].NodeID != "node-linux" {
		t.Fatalf("recovered node order = %q, %q; want selected order", got.Nodes[0].NodeID, got.Nodes[1].NodeID)
	}
}

func TestServeRecoverySnapshotFromSummaryRejectsOfflineAndStaleTelemetry(t *testing.T) {
	now := time.Now().UTC()
	selected := []types.GenerativeNodeCapability{
		{NodeID: "node-win", NodeName: "Worker-Windows"},
		{NodeID: "node-linux", NodeName: "Worker-Linux"},
	}
	summary := types.GenerativeFabricSummary{Nodes: []types.GenerativeNodeCapability{
		{NodeID: "node-win", NodeName: "Worker-Windows", NodeState: "OFFLINE", LastSeen: now, TelemetryUpdatedAt: now},
		{NodeID: "node-linux", NodeName: "Worker-Linux", NodeState: "READY", LastSeen: now, TelemetryUpdatedAt: now.Add(-generativeReadinessFreshness - time.Second)},
	}}

	got := serveRecoverySnapshotFromSummary(summary, selected, now)
	if len(got.Nodes) != 0 {
		t.Fatalf("got %d recovered nodes, want 0", len(got.Nodes))
	}
	joined := strings.Join(got.Issues, "; ")
	if !strings.Contains(joined, "Worker-Windows: heartbeat is offline") {
		t.Fatalf("missing offline issue: %q", joined)
	}
	if !strings.Contains(joined, "Worker-Linux: telemetry is stale") {
		t.Fatalf("missing stale telemetry issue: %q", joined)
	}
}

func TestServeRecoverySnapshotFromSummaryReportsMissingSelectedNode(t *testing.T) {
	now := time.Now().UTC()
	selected := []types.GenerativeNodeCapability{{NodeID: "node-win", NodeName: "Worker-Windows"}}
	got := serveRecoverySnapshotFromSummary(types.GenerativeFabricSummary{}, selected, now)
	if len(got.Nodes) != 0 || len(got.Issues) != 1 {
		t.Fatalf("unexpected snapshot: %+v", got)
	}
	if !strings.Contains(got.Issues[0], "not present in controller inventory") {
		t.Fatalf("unexpected issue: %q", got.Issues[0])
	}
}
