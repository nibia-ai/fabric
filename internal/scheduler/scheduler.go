package scheduler

import (
	"fmt"
	"math"
	"sort"
	"strings"

	"github.com/nibia-ai/fabric/internal/types"
)

const (
	StatePlaced        = "PLACED"
	StateUnschedulable = "UNSCHEDULABLE"
)

func Place(nodes []types.NodeStatus, req types.JobRequirements) types.PlacementDecision {
	return PlaceWithHints(nodes, req, types.PlacementHints{})
}

func PlaceWithHints(
	nodes []types.NodeStatus,
	req types.JobRequirements,
	hints types.PlacementHints,
) types.PlacementDecision {
	return placeWithHints(nodes, req, hints, false)
}

// PlaceLifecycleCleanup permits a cleanup job that is explicitly pinned to a
// node to remain placeable while that node's heartbeat is temporarily stale.
// This is intentionally narrow: ordinary workloads must never target OFFLINE
// nodes. The returning Agent can claim the cleanup lease as soon as its control
// plane recovers.
func PlaceLifecycleCleanup(
	nodes []types.NodeStatus,
	req types.JobRequirements,
	hints types.PlacementHints,
) types.PlacementDecision {
	return placeWithHints(nodes, req, hints, true)
}

func placeWithHints(
	nodes []types.NodeStatus,
	req types.JobRequirements,
	hints types.PlacementHints,
	lifecycleCleanup bool,
) types.PlacementDecision {
	candidates := make([]types.CandidateScore, 0, len(nodes))

	eligible := make([]types.NodeStatus, 0, len(nodes))
	for _, n := range nodes {
		reasons := rejectionReasons(n, req, lifecycleCleanup)
		if len(reasons) == 0 {
			eligible = append(eligible, n)
		}
		candidates = append(candidates, types.CandidateScore{
			NodeID:           n.Node.ID,
			NodeName:         n.Node.Name,
			Eligible:         len(reasons) == 0,
			RejectionReasons: reasons,
		})
	}

	maxCPU := 0.0
	maxRAM := 0.0
	for _, n := range eligible {
		if raw := rawCPUCapacity(n.Node); raw > maxCPU {
			maxCPU = raw
		}
		if raw := float64(n.Node.MemoryTotalMB); raw > maxRAM {
			maxRAM = raw
		}
	}

	for i := range candidates {
		if !candidates[i].Eligible {
			continue
		}
		var node types.NodeStatus
		for _, n := range eligible {
			if n.Node.ID == candidates[i].NodeID {
				node = n
				break
			}
		}

		capacity := capacityScore(node.Node, maxCPU, maxRAM)
		availability := availabilityScore(node)
		affinity := 1.0
		placement := capacity * availability

		if len(hints.NodeAffinity) > 0 {
			// Model/runtime intelligence is a soft preference, not a hard
			// replacement for current resource availability. An affinity of
			// 0..1 adjusts the base placement by at most 20%.
			affinity = 0.5
			if value, ok := hints.NodeAffinity[node.Node.ID]; ok {
				affinity = clamp01(value)
			}
			placement *= 0.80 + 0.20*affinity
		}
		placement = clamp01(placement)

		candidates[i].CapacityScore = capacity
		candidates[i].AvailabilityScore = availability
		candidates[i].AffinityScore = affinity
		candidates[i].PlacementScore = placement
	}

	sort.SliceStable(candidates, func(i, j int) bool {
		if candidates[i].Eligible != candidates[j].Eligible {
			return candidates[i].Eligible
		}
		if math.Abs(candidates[i].PlacementScore-candidates[j].PlacementScore) > 0.000001 {
			return candidates[i].PlacementScore > candidates[j].PlacementScore
		}
		if math.Abs(candidates[i].CapacityScore-candidates[j].CapacityScore) > 0.000001 {
			return candidates[i].CapacityScore > candidates[j].CapacityScore
		}
		return candidates[i].NodeName < candidates[j].NodeName
	})

	decision := types.PlacementDecision{Candidates: candidates}
	for _, c := range candidates {
		if c.Eligible {
			decision.SelectedNodeID = c.NodeID
			decision.SelectedNodeName = c.NodeName
			break
		}
	}
	return decision
}

func rejectionReasons(n types.NodeStatus, req types.JobRequirements, lifecycleCleanup bool) []string {
	var reasons []string
	node := n.Node

	if want := strings.TrimSpace(req.RequiredNodeID); want != "" &&
		!strings.EqualFold(node.ID, want) {
		reasons = append(reasons, fmt.Sprintf(
			"requires node_id=%s; node is %s", want, node.ID,
		))
	}

	for _, excluded := range req.ExcludedNodeIDs {
		if strings.EqualFold(strings.TrimSpace(excluded), node.ID) {
			reasons = append(reasons, fmt.Sprintf("node_id=%s is excluded for retry", node.ID))
			break
		}
	}

	if strings.EqualFold(n.State, "offline") && !lifecycleCleanup {
		reasons = append(reasons, "node is offline")
	}

	resourceState := strings.ToUpper(strings.TrimSpace(node.ResourceState))
	switch resourceState {
	case "PRESSURED":
		if !lifecycleCleanup {
			reasons = append(reasons, "node is memory/resource pressured")
		}
	case "BUSY":
		if !req.AllowBusy && !lifecycleCleanup {
			reasons = append(reasons, "node is busy and allow_busy=false")
		}
	}

	if req.MinPhysicalCores > 0 && node.CPUPhysical < req.MinPhysicalCores {
		reasons = append(reasons, fmt.Sprintf(
			"requires >=%d physical cores; node has %d",
			req.MinPhysicalCores, node.CPUPhysical,
		))
	}
	if req.MinLogicalCores > 0 && node.CPULogical < req.MinLogicalCores {
		reasons = append(reasons, fmt.Sprintf(
			"requires >=%d logical cores; node has %d",
			req.MinLogicalCores, node.CPULogical,
		))
	}
	if req.MinMemoryMB > 0 && node.MemoryAvailableMB < req.MinMemoryMB {
		reasons = append(reasons, fmt.Sprintf(
			"requires >=%d MB available RAM; node has %d MB",
			req.MinMemoryMB, node.MemoryAvailableMB,
		))
	}

	if want := strings.TrimSpace(req.OS); want != "" && !strings.EqualFold(node.OS, want) {
		reasons = append(reasons, fmt.Sprintf(
			"requires os=%s; node is %s", want, node.OS,
		))
	}
	if want := strings.TrimSpace(req.Arch); want != "" && !strings.EqualFold(node.Arch, want) {
		reasons = append(reasons, fmt.Sprintf(
			"requires arch=%s; node is %s", want, node.Arch,
		))
	}

	have := make(map[string]bool, len(node.Capabilities))
	for _, cap := range node.Capabilities {
		have[strings.ToLower(strings.TrimSpace(cap))] = true
	}
	for _, want := range req.Capabilities {
		want = strings.ToLower(strings.TrimSpace(want))
		if want != "" && !have[want] {
			reasons = append(reasons, "missing capability="+want)
		}
	}

	return reasons
}

func rawCPUCapacity(n types.Node) float64 {
	physical := n.CPUPhysical
	logical := n.CPULogical

	if physical <= 0 {
		physical = logical
	}
	if logical < physical {
		logical = physical
	}
	if physical <= 0 {
		return 1
	}

	// A physical core counts fully. Extra SMT/hyperthreads count as half a core.
	// This avoids treating 6C/12T as equal to a true 12-core CPU.
	return float64(physical) + 0.5*float64(logical-physical)
}

func capacityScore(n types.Node, maxCPU, maxRAM float64) float64 {
	cpuNorm := 1.0
	if maxCPU > 0 {
		cpuNorm = rawCPUCapacity(n) / maxCPU
	}

	ramNorm := 1.0
	if maxRAM > 0 {
		ramNorm = float64(n.MemoryTotalMB) / maxRAM
	}

	// CPU is the main resource in the current CPU-first NIBIA scheduler.
	return clamp01(0.75*cpuNorm + 0.25*ramNorm)
}

func availabilityScore(n types.NodeStatus) float64 {
	node := n.Node

	cpuFree := 0.5
	if node.CPUUsedPct >= 0 {
		cpuFree = clamp01(1 - node.CPUUsedPct/100)
	}

	memFree := 0.5
	if node.MemoryTotalMB > 0 {
		memFree = clamp01(float64(node.MemoryAvailableMB) / float64(node.MemoryTotalMB))
	}

	pressure := memFree
	if node.MemoryPressurePct >= 0 {
		pressure = clamp01(node.MemoryPressurePct / 100)
	}

	stateFactor := 1.0
	if strings.EqualFold(n.State, "offline") {
		stateFactor = 0
	}
	switch strings.ToUpper(strings.TrimSpace(node.ResourceState)) {
	case "BUSY":
		stateFactor *= 0.65
	case "PRESSURED":
		stateFactor = 0
	}

	score := (0.55*cpuFree + 0.35*memFree + 0.10*pressure) * stateFactor
	return clamp01(score)
}

func clamp01(v float64) float64 {
	if v < 0 {
		return 0
	}
	if v > 1 {
		return 1
	}
	return v
}
