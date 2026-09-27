package main

import (
	"context"
	"errors"
	"fmt"
	"math"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/nibia-ai/fabric/internal/executor"
	"github.com/nibia-ai/fabric/internal/types"
)

type remoteGenerativeCandidate struct {
	Node   types.GenerativeNodeCapability
	Relay  types.GenerativeRelayStatus
	Device llamaDevice
}

type plannedGenerativeDevice struct {
	Device           llamaDevice
	NodeID           string
	NodeName         string
	RelayEndpoint    string
	RuntimeFreeMiB   uint64
	OSAvailableMiB   uint64
	CapacityBasisMiB uint64
	ReserveMiB       uint64
	ReserveSource    string
	UsableMiB        uint64
	Share            float64
	Local            bool
}

type automaticGenerativePlanN struct {
	Classification       string
	Distributed          bool
	ModelMiB             uint64
	ReserveMiB           uint64 // legacy fixed-reserve compatibility
	ReservePolicySummary string
	Selected             []plannedGenerativeDevice
	AggregateMiB         uint64
}

func getGenerativeFabricSummary(controller string) (types.GenerativeFabricSummary, error) {
	var summary types.GenerativeFabricSummary
	err := apiJSON("GET", controller, "/v1/generative/fabric", nil, &summary)
	return summary, err
}

func localGenerativeNode(summary types.GenerativeFabricSummary) (types.GenerativeNodeCapability, bool) {
	host, _ := os.Hostname()
	for _, n := range summary.Nodes {
		if host != "" && strings.EqualFold(strings.TrimSpace(n.Hostname), strings.TrimSpace(host)) {
			return n, true
		}
	}
	// Fallback: on a single coordinator-capable node matching this platform,
	// use it as the coordinator identity. Hostname matching is preferred.
	var candidates []types.GenerativeNodeCapability
	for _, n := range summary.Nodes {
		if n.CoordinatorCapable && !strings.EqualFold(n.NodeState, "OFFLINE") {
			candidates = append(candidates, n)
		}
	}
	if len(candidates) == 1 {
		return candidates[0], true
	}
	return types.GenerativeNodeCapability{}, false
}

func parseNodeSelection(node, nodes string) []string {
	var out []string
	seen := map[string]bool{}
	add := func(v string) {
		v = strings.TrimSpace(v)
		if v == "" || seen[strings.ToLower(v)] {
			return
		}
		seen[strings.ToLower(v)] = true
		out = append(out, v)
	}
	add(node)
	for _, v := range strings.Split(nodes, ",") {
		add(v)
	}
	return out
}

const generativeReadinessFreshness = 12 * time.Second

var errCleanupTargetUnavailable = errors.New("cleanup target node unavailable")

// cleanupNodeUnavailable reports whether the Controller has declared a node
// unavailable for control-plane cleanup. The Controller is the liveness
// authority; the CLI must not impose a shorter heartbeat-staleness threshold
// here because a heavily loaded but still-online Agent may briefly exceed it.
// A graceful Agent shutdown is marked OFFLINE immediately by the Controller,
// while abrupt loss is handled by the Controller's own offline threshold.
func cleanupNodeUnavailable(n types.GenerativeNodeCapability, _ time.Time) bool {
	if strings.EqualFold(strings.TrimSpace(n.NodeState), "OFFLINE") {
		return true
	}
	return n.LastSeen.IsZero()
}

func cleanupTargetAvailable(controller, nodeID string) (bool, error) {
	summary, err := getGenerativeFabricSummary(controller)
	if err != nil {
		return false, err
	}
	for _, n := range summary.Nodes {
		if n.NodeID == nodeID {
			return !cleanupNodeUnavailable(n, time.Now()), nil
		}
	}
	return false, nil
}

func waitScheduledCleanupJob(controller string, job types.ScheduledJob, timeout time.Duration, nodeID string) (types.ScheduledJob, error) {
	deadline := time.Now().Add(timeout)
	for !terminalJobState(job.State) {
		if strings.EqualFold(job.State, "UNSCHEDULABLE") {
			return job, fmt.Errorf("job %s is unschedulable", job.ID)
		}
		available, availabilityErr := cleanupTargetAvailable(controller, nodeID)
		if availabilityErr == nil && !available {
			cancelScheduledJobBestEffort(controller, job.ID)
			return job, fmt.Errorf("%w: %s", errCleanupTargetUnavailable, nodeID)
		}
		if time.Now().After(deadline) {
			return job, fmt.Errorf("timeout waiting for job %s", job.ID)
		}
		time.Sleep(500 * time.Millisecond)
		if err := apiJSON(http.MethodGet, controller, "/v1/jobs/"+job.ID, nil, &job); err != nil {
			return job, err
		}
	}
	return job, nil
}

func unavailableGenerativeNodeNames(controller string, nodes []types.GenerativeNodeCapability) []string {
	summary, err := getGenerativeFabricSummary(controller)
	if err != nil {
		return nil
	}
	byID := make(map[string]types.GenerativeNodeCapability, len(summary.Nodes))
	for _, n := range summary.Nodes {
		byID[n.NodeID] = n
	}
	var out []string
	now := time.Now()
	for _, selected := range nodes {
		n, ok := byID[selected.NodeID]
		if !ok || cleanupNodeUnavailable(n, now) {
			name := strings.TrimSpace(selected.NodeName)
			if name == "" {
				name = selected.NodeID
			}
			out = append(out, name)
		}
	}
	return out
}

type recoveringGenerativeCandidate struct {
	Node   types.GenerativeNodeCapability
	Reason string
}

type generativeCandidateSnapshot struct {
	Ready           []types.GenerativeNodeCapability
	Recovering      []recoveringGenerativeCandidate
	Local           types.GenerativeNodeCapability
	TotalCandidates int
}

func generativeNodeFreshnessIssue(n types.GenerativeNodeCapability, now time.Time) string {
	if strings.EqualFold(strings.TrimSpace(n.NodeState), "OFFLINE") {
		return "heartbeat is offline"
	}
	if n.LastSeen.IsZero() {
		return "heartbeat timestamp is unavailable"
	}
	if age := now.Sub(n.LastSeen); age > generativeReadinessFreshness {
		return fmt.Sprintf("heartbeat is stale (%s old)", age.Round(time.Second))
	}
	if n.TelemetryUpdatedAt.IsZero() {
		return "telemetry timestamp is unavailable"
	}
	if age := now.Sub(n.TelemetryUpdatedAt); age > generativeReadinessFreshness {
		return fmt.Sprintf("telemetry is stale (%s old)", age.Round(time.Second))
	}
	return ""
}

func generativeNodeReady(n types.GenerativeNodeCapability, now time.Time) bool {
	return generativeNodeFreshnessIssue(n, now) == ""
}

func sortGenerativeNodes(out []types.GenerativeNodeCapability) {
	sort.Slice(out, func(i, j int) bool {
		if out[i].NodeName != out[j].NodeName {
			return out[i].NodeName < out[j].NodeName
		}
		return out[i].NodeID < out[j].NodeID
	})
}

// resolveGenerativeCandidateSnapshot separates candidate membership from
// readiness. --node/--nodes define allowed candidates, not mandatory workers.
// Explicitly requested OFFLINE nodes remain visible as RECOVERING candidates;
// auto-discovery ignores long-offline historical nodes but can still recover a
// recently stale online candidate.
func resolveGenerativeCandidateSnapshot(controller, node, nodes string) (generativeCandidateSnapshot, error) {
	summary, err := getGenerativeFabricSummary(controller)
	if err != nil {
		return generativeCandidateSnapshot{}, err
	}
	local, _ := localGenerativeNode(summary)
	requested := parseNodeSelection(node, nodes)
	byName := map[string]types.GenerativeNodeCapability{}
	for _, n := range summary.Nodes {
		byName[strings.ToLower(n.NodeID)] = n
		byName[strings.ToLower(n.NodeName)] = n
	}

	var candidates []types.GenerativeNodeCapability
	if len(requested) > 0 {
		for _, r := range requested {
			n, ok := byName[strings.ToLower(r)]
			if !ok {
				return generativeCandidateSnapshot{Local: local}, fmt.Errorf("node %q not found in Fabric", r)
			}
			if local.NodeID != "" && n.NodeID == local.NodeID {
				continue
			}
			if !n.WorkerCapable {
				return generativeCandidateSnapshot{Local: local}, fmt.Errorf("node %s is not RPC-worker capable", n.NodeName)
			}
			candidates = append(candidates, n)
		}
	} else {
		for _, n := range summary.Nodes {
			if local.NodeID != "" && n.NodeID == local.NodeID {
				continue
			}
			if !n.WorkerCapable || strings.EqualFold(strings.TrimSpace(n.NodeState), "OFFLINE") {
				continue
			}
			candidates = append(candidates, n)
		}
	}

	now := time.Now().UTC()
	snap := generativeCandidateSnapshot{Local: local, TotalCandidates: len(candidates)}
	for _, n := range candidates {
		if issue := generativeNodeFreshnessIssue(n, now); issue != "" {
			snap.Recovering = append(snap.Recovering, recoveringGenerativeCandidate{Node: n, Reason: issue})
			continue
		}
		snap.Ready = append(snap.Ready, n)
	}
	sortGenerativeNodes(snap.Ready)
	sort.Slice(snap.Recovering, func(i, j int) bool {
		return snap.Recovering[i].Node.NodeName < snap.Recovering[j].Node.NodeName
	})
	return snap, nil
}

func recoveringSummary(items []recoveringGenerativeCandidate) string {
	parts := make([]string, 0, len(items))
	for _, item := range items {
		parts = append(parts, fmt.Sprintf("%s: %s", item.Node.NodeName, item.Reason))
	}
	return strings.Join(parts, "; ")
}

// resolveGenerativeCandidatesWithGrace is used by generic discovery/run paths
// that do not yet know the model's required capacity. If at least one requested
// candidate is fresh it proceeds with the fresh subset; unavailable candidates
// no longer make the whole candidate set mandatory. If none are fresh, it
// waits briefly for recovery.
func resolveGenerativeCandidatesWithGrace(ctx context.Context, controller, node, nodes string, grace time.Duration) ([]types.GenerativeNodeCapability, types.GenerativeNodeCapability, error) {
	started := time.Now()
	warned := false
	for {
		snap, err := resolveGenerativeCandidateSnapshot(controller, node, nodes)
		if err != nil {
			return nil, snap.Local, err
		}
		if len(snap.Ready) > 0 {
			if warned {
				fmt.Printf("      ✓ worker readiness recovered after %s\n", time.Since(started).Round(100*time.Millisecond))
			}
			return snap.Ready, snap.Local, nil
		}
		if len(snap.Recovering) == 0 {
			return nil, snap.Local, fmt.Errorf("no fresh remote llama.cpp RPC worker nodes are available")
		}
		if grace <= 0 || time.Since(started) >= grace {
			return nil, snap.Local, fmt.Errorf("no fresh remote worker recovered within %s (%s)", grace, recoveringSummary(snap.Recovering))
		}
		if !warned {
			warned = true
			fmt.Printf("      ⚠ Worker candidate is RECOVERING (%s); waiting up to %s for fresh readiness...\n", recoveringSummary(snap.Recovering), grace)
		}
		if err := waitForContext(ctx, 750*time.Millisecond); err != nil {
			return nil, snap.Local, err
		}
	}
}

func estimatedReadyCapacityMiB(ready []types.GenerativeNodeCapability, reserveMiB, localUsableMiB uint64) uint64 {
	return estimatedReadyCapacityMiBWithPolicy(ready, fixedMemoryReservePolicy(reserveMiB), localUsableMiB)
}

func estimatedReadyCapacityMiBWithPolicy(ready []types.GenerativeNodeCapability, policy memoryReservePolicy, localUsableMiB uint64) uint64 {
	capacity := localUsableMiB
	for _, n := range ready {
		reserve := policy.reserveForNode(n, n.MemoryAvailableMB, false)
		capacity += subtractReserve(n.MemoryAvailableMB, reserve.MiB)
	}
	return capacity
}

func readyCandidateSetSatisfiesCapacity(ready []types.GenerativeNodeCapability, reserveMiB, localUsableMiB, modelMiB uint64, forceDistributed bool) bool {
	return readyCandidateSetSatisfiesCapacityWithPolicy(ready, fixedMemoryReservePolicy(reserveMiB), localUsableMiB, modelMiB, forceDistributed)
}

func readyCandidateSetSatisfiesCapacityWithPolicy(ready []types.GenerativeNodeCapability, policy memoryReservePolicy, localUsableMiB, modelMiB uint64, forceDistributed bool) bool {
	if forceDistributed && len(ready) == 0 {
		return false
	}
	return estimatedReadyCapacityMiBWithPolicy(ready, policy, localUsableMiB) >= modelMiB
}

// resolveGenerativeCandidatesForCapacityWithGrace is retained for fixed-reserve
// compatibility and tests. New CLI paths use the policy-aware variant below.
func resolveGenerativeCandidatesForCapacityWithGrace(
	ctx context.Context,
	controller, node, nodes string,
	grace time.Duration,
	reserveMiB, localUsableMiB, modelMiB uint64,
	forceDistributed bool,
) ([]types.GenerativeNodeCapability, types.GenerativeNodeCapability, int, int, error) {
	return resolveGenerativeCandidatesForCapacityWithGracePolicy(
		ctx, controller, node, nodes, grace,
		fixedMemoryReservePolicy(reserveMiB), localUsableMiB, modelMiB, forceDistributed,
	)
}

// resolveGenerativeCandidatesForCapacityWithGracePolicy is the SERVE readiness
// gate. It only waits when the currently fresh candidate subset cannot satisfy
// the requested model under the active per-node memory reservation policy.
func resolveGenerativeCandidatesForCapacityWithGracePolicy(
	ctx context.Context,
	controller, node, nodes string,
	grace time.Duration,
	policy memoryReservePolicy,
	localUsableMiB, modelMiB uint64,
	forceDistributed bool,
) ([]types.GenerativeNodeCapability, types.GenerativeNodeCapability, int, int, error) {
	started := time.Now()
	warned := false
	for {
		snap, err := resolveGenerativeCandidateSnapshot(controller, node, nodes)
		if err != nil {
			return nil, snap.Local, snap.TotalCandidates, len(snap.Recovering), err
		}
		if readyCandidateSetSatisfiesCapacityWithPolicy(snap.Ready, policy, localUsableMiB, modelMiB, forceDistributed) {
			if warned {
				fmt.Printf("      ✓ sufficient worker readiness recovered after %s\n", time.Since(started).Round(100*time.Millisecond))
			}
			return snap.Ready, snap.Local, snap.TotalCandidates, len(snap.Recovering), nil
		}
		readyMiB := estimatedReadyCapacityMiBWithPolicy(snap.Ready, policy, localUsableMiB)
		if len(snap.Recovering) == 0 {
			return snap.Ready, snap.Local, snap.TotalCandidates, 0, fmt.Errorf(
				"insufficient ready capacity: %.2f GiB currently ready for a %.2f GiB model",
				float64(readyMiB)/1024,
				float64(modelMiB)/1024,
			)
		}
		if grace <= 0 || time.Since(started) >= grace {
			return snap.Ready, snap.Local, snap.TotalCandidates, len(snap.Recovering), fmt.Errorf(
				"insufficient ready capacity after %s: %.2f GiB ready for a %.2f GiB model; recovering candidates: %s",
				grace,
				float64(readyMiB)/1024,
				float64(modelMiB)/1024,
				recoveringSummary(snap.Recovering),
			)
		}
		if !warned {
			warned = true
			fmt.Printf("      ⚠ Additional worker capacity is RECOVERING (%s); waiting up to %s...\n", recoveringSummary(snap.Recovering), grace)
		}
		if err := waitForContext(ctx, 750*time.Millisecond); err != nil {
			return nil, snap.Local, snap.TotalCandidates, len(snap.Recovering), err
		}
	}
}

func attachRemoteDevices(devices []llamaDevice, nodes []types.GenerativeNodeCapability, relays []types.GenerativeRelayStatus) []remoteGenerativeCandidate {
	byEndpoint := map[string]llamaDevice{}
	for _, d := range devices {
		if d.Remote {
			byEndpoint[strings.TrimSpace(d.Name)] = d
		}
	}
	out := make([]remoteGenerativeCandidate, 0, len(nodes))
	for i, n := range nodes {
		if i >= len(relays) {
			break
		}
		relay := relays[i]
		d, ok := byEndpoint[strings.TrimSpace(relay.LocalEndpoint)]
		if !ok {
			continue
		}
		out = append(out, remoteGenerativeCandidate{Node: n, Relay: relay, Device: d})
	}
	return out
}

func selectLocalExecutionDevice(localNode types.GenerativeNodeCapability, devices []llamaDevice) (llamaDevice, bool) {
	var local llamaDevice
	for _, d := range devices {
		if d.Remote || d.FreeMiB == 0 {
			continue
		}
		if d.FreeMiB > local.FreeMiB {
			local = d
		}
	}
	if local.ID != "" {
		return local, true
	}
	// llama.cpp --list-devices enumerates offload devices, not the ordinary
	// host CPU. On CPU-only Linux/Windows it therefore legitimately returns
	// "(none)" even though CPU inference is available. Represent that local
	// execution domain explicitly inside NIBIA and size it from fresh OS RAM
	// telemetry. The runtime is invoked with --device none, never "CPU0".
	if localNode.MemoryAvailableMB > 0 {
		return llamaDevice{
			ID:       "CPU0",
			Name:     "System CPU / RAM",
			TotalMiB: localNode.MemoryTotalMB,
			CPUOnly:  true,
		}, true
	}
	return llamaDevice{}, false
}

func localCapacityBasis(local llamaDevice, localNode types.GenerativeNodeCapability) uint64 {
	if local.CPUOnly {
		return localNode.MemoryAvailableMB
	}
	basis := local.FreeMiB
	if localNode.MemoryAvailableMB > 0 && (basis == 0 || localNode.MemoryAvailableMB < basis) {
		basis = localNode.MemoryAvailableMB
	}
	return basis
}

func planLocalGenerativeRunN(modelBytes int64, reserveMiB uint64, localNode types.GenerativeNodeCapability, devices []llamaDevice) (automaticGenerativePlanN, bool, error) {
	return planLocalGenerativeRunNWithPolicy(modelBytes, fixedMemoryReservePolicy(reserveMiB), localNode, devices)
}

func planLocalGenerativeRunNWithPolicy(modelBytes int64, policy memoryReservePolicy, localNode types.GenerativeNodeCapability, devices []llamaDevice) (automaticGenerativePlanN, bool, error) {
	local, ok := selectLocalExecutionDevice(localNode, devices)
	if !ok {
		return automaticGenerativePlanN{}, false, fmt.Errorf("no local execution capacity was found (llama.cpp offload device unavailable and OS memory telemetry missing)")
	}
	modelMiB := uint64(math.Ceil(float64(modelBytes) / (1024 * 1024)))
	localBasis := localCapacityBasis(local, localNode)
	reserve := policy.reserveForNode(localNode, localBasis, true)
	localUsable := subtractReserve(localBasis, reserve.MiB)
	plan := automaticGenerativePlanN{
		ModelMiB:             modelMiB,
		ReservePolicySummary: policy.summary(),
		AggregateMiB:         localUsable,
		Selected: []plannedGenerativeDevice{{
			Device: local, RuntimeFreeMiB: local.FreeMiB, OSAvailableMiB: localNode.MemoryAvailableMB,
			CapacityBasisMiB: localBasis, ReserveMiB: reserve.MiB, ReserveSource: reserve.Source,
			UsableMiB: localUsable, Local: true,
		}},
	}
	if policy.GlobalFixedMiB != nil {
		plan.ReserveMiB = *policy.GlobalFixedMiB
	}
	fits := modelMiB <= localUsable
	if fits {
		plan.Classification = "FITS-LOCAL"
		plan.Selected[0].Share = 1
	}
	return plan, fits, nil
}

type estimatedRemoteNode struct {
	Node      types.GenerativeNodeCapability
	UsableMiB uint64
}

func selectRemoteNodesForCapacity(nodes []types.GenerativeNodeCapability, reserveMiB, localUsableMiB, modelMiB uint64, forceDistributed bool) ([]types.GenerativeNodeCapability, []types.GenerativeNodeCapability) {
	return selectRemoteNodesForCapacityWithPolicy(nodes, fixedMemoryReservePolicy(reserveMiB), localUsableMiB, modelMiB, forceDistributed)
}

// selectRemoteNodesForCapacityWithPolicy uses current Agent memory telemetry to
// avoid starting RPC workers that cannot contribute to the requested model.
// The final llama.cpp discovery remains authoritative.
func selectRemoteNodesForCapacityWithPolicy(nodes []types.GenerativeNodeCapability, policy memoryReservePolicy, localUsableMiB, modelMiB uint64, forceDistributed bool) ([]types.GenerativeNodeCapability, []types.GenerativeNodeCapability) {
	estimated := make([]estimatedRemoteNode, 0, len(nodes))
	for _, n := range nodes {
		reserve := policy.reserveForNode(n, n.MemoryAvailableMB, false)
		estimated = append(estimated, estimatedRemoteNode{Node: n, UsableMiB: subtractReserve(n.MemoryAvailableMB, reserve.MiB)})
	}
	sort.SliceStable(estimated, func(i, j int) bool {
		if estimated[i].UsableMiB != estimated[j].UsableMiB {
			return estimated[i].UsableMiB > estimated[j].UsableMiB
		}
		return estimated[i].Node.NodeName < estimated[j].Node.NodeName
	})

	capacity := localUsableMiB
	selectedCount := 0
	for i, e := range estimated {
		needRemote := capacity < modelMiB || (forceDistributed && selectedCount == 0)
		if !needRemote {
			break
		}
		capacity += e.UsableMiB
		selectedCount = i + 1
	}
	if selectedCount == 0 && forceDistributed && len(estimated) > 0 {
		selectedCount = 1
	}
	selected := make([]types.GenerativeNodeCapability, 0, selectedCount)
	remaining := make([]types.GenerativeNodeCapability, 0, len(estimated)-selectedCount)
	for i, e := range estimated {
		if i < selectedCount {
			selected = append(selected, e.Node)
		} else {
			remaining = append(remaining, e.Node)
		}
	}
	return selected, remaining
}

func planAutomaticGenerativeRunN(modelBytes int64, reserveMiB uint64, forceDistributed bool, localNode types.GenerativeNodeCapability, devices []llamaDevice, remotes []remoteGenerativeCandidate) (automaticGenerativePlanN, error) {
	return planAutomaticGenerativeRunNWithPolicy(modelBytes, fixedMemoryReservePolicy(reserveMiB), forceDistributed, localNode, devices, remotes)
}

func planAutomaticGenerativeRunNWithPolicy(modelBytes int64, policy memoryReservePolicy, forceDistributed bool, localNode types.GenerativeNodeCapability, devices []llamaDevice, remotes []remoteGenerativeCandidate) (automaticGenerativePlanN, error) {
	local, ok := selectLocalExecutionDevice(localNode, devices)
	if !ok {
		return automaticGenerativePlanN{}, fmt.Errorf("no local execution capacity was found (llama.cpp offload device unavailable and OS memory telemetry missing)")
	}
	modelMiB := uint64(math.Ceil(float64(modelBytes) / (1024 * 1024)))
	localBasis := localCapacityBasis(local, localNode)
	localReserve := policy.reserveForNode(localNode, localBasis, true)
	localUsable := subtractReserve(localBasis, localReserve.MiB)
	plan := automaticGenerativePlanN{ModelMiB: modelMiB, ReservePolicySummary: policy.summary()}
	if policy.GlobalFixedMiB != nil {
		plan.ReserveMiB = *policy.GlobalFixedMiB
	}
	plan.Selected = append(plan.Selected, plannedGenerativeDevice{
		Device: local, RuntimeFreeMiB: local.FreeMiB, OSAvailableMiB: localNode.MemoryAvailableMB,
		CapacityBasisMiB: localBasis, ReserveMiB: localReserve.MiB, ReserveSource: localReserve.Source,
		UsableMiB: localUsable, Local: true,
	})
	plan.AggregateMiB = localUsable
	if modelMiB <= localUsable && !forceDistributed {
		plan.Classification = "FITS-LOCAL"
		plan.Selected[0].Share = 1
		return plan, nil
	}
	type scored struct {
		c       remoteGenerativeCandidate
		basis   uint64
		reserve resolvedMemoryReserve
		usable  uint64
	}
	candidates := make([]scored, 0, len(remotes))
	for _, r := range remotes {
		basis := r.Device.FreeMiB
		if r.Node.MemoryAvailableMB > 0 && (basis == 0 || r.Node.MemoryAvailableMB < basis) {
			basis = r.Node.MemoryAvailableMB
		}
		reserve := policy.reserveForNode(r.Node, basis, false)
		usable := subtractReserve(basis, reserve.MiB)
		if usable == 0 {
			continue
		}
		candidates = append(candidates, scored{c: r, basis: basis, reserve: reserve, usable: usable})
	}
	sort.SliceStable(candidates, func(i, j int) bool {
		if candidates[i].usable != candidates[j].usable {
			return candidates[i].usable > candidates[j].usable
		}
		return candidates[i].c.Node.NodeName < candidates[j].c.Node.NodeName
	})
	for _, x := range candidates {
		plan.Selected = append(plan.Selected, plannedGenerativeDevice{
			Device: x.c.Device, NodeID: x.c.Node.NodeID, NodeName: x.c.Node.NodeName, RelayEndpoint: x.c.Relay.LocalEndpoint,
			RuntimeFreeMiB: x.c.Device.FreeMiB, OSAvailableMiB: x.c.Node.MemoryAvailableMB, CapacityBasisMiB: x.basis,
			ReserveMiB: x.reserve.MiB, ReserveSource: x.reserve.Source, UsableMiB: x.usable,
		})
		plan.AggregateMiB += x.usable
		if plan.AggregateMiB >= modelMiB && (!forceDistributed || len(plan.Selected) >= 2) {
			break
		}
	}
	if len(plan.Selected) < 2 {
		return automaticGenerativePlanN{}, fmt.Errorf("no remote RPC device with usable memory was found")
	}
	plan.Distributed = true
	if plan.AggregateMiB < modelMiB {
		plan.Classification = "INSUFFICIENT-FABRIC-CAPACITY"
	} else if modelMiB > localUsable {
		plan.Classification = "CAPACITY-EXPANDING"
	} else {
		plan.Classification = "DISTRIBUTED-FORCED"
	}
	if local.CPUOnly && plan.Distributed {
		// CPU0 is host CPU/RAM, not a llama.cpp offload device. Treat the
		// Primary safe RAM budget as the local model fraction, then distribute
		// the remaining model fraction across selected RPC workers according to
		// their safe usable capacity. This keeps product-facing shares meaningful
		// while llama.cpp remains authoritative for exact layer placement.
		localShareMiB := minUint64(localUsable, modelMiB)
		plan.Selected[0].Share = float64(localShareMiB) / float64(modelMiB)
		remainingMiB := modelMiB - localShareMiB
		var remoteBase uint64
		for i := 1; i < len(plan.Selected); i++ {
			remoteBase += plan.Selected[i].UsableMiB
		}
		if remoteBase > 0 && remainingMiB > 0 {
			for i := 1; i < len(plan.Selected); i++ {
				plan.Selected[i].Share = (float64(remainingMiB) / float64(modelMiB)) *
					(float64(plan.Selected[i].UsableMiB) / float64(remoteBase))
			}
		}
	} else {
		var shareBase uint64
		for _, d := range plan.Selected {
			shareBase += d.UsableMiB
		}
		if shareBase > 0 {
			for i := range plan.Selected {
				plan.Selected[i].Share = float64(plan.Selected[i].UsableMiB) / float64(shareBase)
			}
		}
	}
	return plan, nil
}

func buildAutomaticLlamaArgsN(model, prompt string, tokens, ctxSize int, loadMode string, plan automaticGenerativePlanN, session bool) []string {
	// --simple-io is critical when llama-cli is embedded as a child process.
	// Without it, llama.cpp may write interactive terminal UI directly to the
	// controlling TTY, bypassing NIBIA's stdout/stderr gate and corrupting the
	// progress renderer. One-shot RUN supplies an explicit prompt; interactive
	// SESSION may intentionally start without one.
	args := []string{"--simple-io", "-m", model}
	if strings.TrimSpace(prompt) != "" {
		args = append(args, "-p", prompt)
	}
	if !session {
		// NIBIA owns the user-facing one-shot UX. Do not echo the input prompt
		// back before the generated response; llama.cpp still emits timing data.
		args = append(args, "--no-display-prompt")
	}
	args = append(args, "-n", strconv.Itoa(tokens), "-c", strconv.Itoa(ctxSize))
	if !plan.Distributed {
		if plan.Selected[0].Device.CPUOnly {
			args = append(args, "-dev", "none", "-ngl", "0", "-fit", "off", "--load-mode", loadMode)
		} else {
			args = append(args, "-dev", plan.Selected[0].Device.ID, "-ngl", "all", "-fit", "off", "--load-mode", loadMode)
		}
	} else {
		endpoints := make([]string, 0, len(plan.Selected)-1)
		deviceIDs := make([]string, 0, len(plan.Selected))
		split := make([]string, 0, len(plan.Selected))
		fitTargets := make([]string, 0, len(plan.Selected)-1)
		remoteIndex := 0
		if !plan.Selected[0].Device.CPUOnly {
			deviceIDs = append(deviceIDs, plan.Selected[0].Device.ID)
			split = append(split, strconv.FormatUint(plan.Selected[0].UsableMiB, 10))
		}
		for _, d := range plan.Selected[1:] {
			endpoints = append(endpoints, d.RelayEndpoint)
			deviceIDs = append(deviceIDs, fmt.Sprintf("RPC%d", remoteIndex))
			split = append(split, strconv.FormatUint(d.UsableMiB, 10))
			fitTargets = append(fitTargets, strconv.FormatUint(d.ReserveMiB, 10))
			remoteIndex++
		}
		args = append([]string{"--rpc", strings.Join(endpoints, ",")}, args...)
		if plan.Selected[0].Device.CPUOnly {
			// CPU0 is intentionally omitted: llama.cpp keeps host CPU as the
			// fallback and receives only actual RPC offload devices.
			args = append(args, "-dev", strings.Join(deviceIDs, ","), "-sm", "layer", "-ts", strings.Join(split, ","), "-ngl", "auto", "-fit", "on")
			if len(fitTargets) > 0 {
				args = append(args, "-fitt", strings.Join(fitTargets, ","))
			}
			args = append(args, "--load-mode", loadMode)
		} else {
			args = append(args, "-dev", strings.Join(deviceIDs, ","), "-sm", "layer", "-ts", strings.Join(split, ","), "-ngl", "all", "-fit", "off", "--load-mode", loadMode)
		}
	}
	if !session {
		args = append(args, "-st")
	}
	return args
}

func selectedRemoteNodeIDs(plan automaticGenerativePlanN) []string {
	var out []string
	for _, d := range plan.Selected {
		if !d.Local && d.NodeID != "" {
			out = append(out, d.NodeID)
		}
	}
	return out
}

func selectedRelayBaselines(plan automaticGenerativePlanN, all []remoteGenerativeCandidate) map[string]types.GenerativeRelayStatus {
	wanted := map[string]bool{}
	for _, d := range plan.Selected {
		if !d.Local {
			wanted[d.NodeID] = true
		}
	}
	out := map[string]types.GenerativeRelayStatus{}
	for _, r := range all {
		if wanted[r.Node.NodeID] {
			out[r.Node.NodeID] = r.Relay
		}
	}
	return out
}

type nodePeak struct {
	NodeName        string
	ModelShare      float64
	MaxCPU          float64
	MaxMemoryPct    float64
	MinAvailableMiB uint64
	Samples         int
}

type nodePeakMonitor struct {
	controller string
	wanted     map[string]*nodePeak
	stop       chan struct{}
	done       chan struct{}
	once       sync.Once
}

func startNodePeakMonitor(controller string, local types.GenerativeNodeCapability, plan automaticGenerativePlanN) *nodePeakMonitor {
	m := &nodePeakMonitor{controller: controller, wanted: map[string]*nodePeak{}, stop: make(chan struct{}), done: make(chan struct{})}
	for _, d := range plan.Selected {
		if d.Local {
			if local.NodeID != "" {
				m.wanted[local.NodeID] = &nodePeak{NodeName: local.NodeName, ModelShare: d.Share, MinAvailableMiB: ^uint64(0)}
			}
		} else {
			m.wanted[d.NodeID] = &nodePeak{NodeName: d.NodeName, ModelShare: d.Share, MinAvailableMiB: ^uint64(0)}
		}
	}
	go func() {
		defer close(m.done)
		tick := time.NewTicker(time.Second)
		defer tick.Stop()
		sample := func() {
			s, err := getGenerativeFabricSummary(controller)
			if err != nil {
				return
			}
			for _, n := range s.Nodes {
				p := m.wanted[n.NodeID]
				if p == nil {
					continue
				}
				if n.CPUUsedPct > p.MaxCPU {
					p.MaxCPU = n.CPUUsedPct
				}
				if n.MemoryUsedPct > p.MaxMemoryPct {
					p.MaxMemoryPct = n.MemoryUsedPct
				}
				if n.MemoryAvailableMB < p.MinAvailableMiB {
					p.MinAvailableMiB = n.MemoryAvailableMB
				}
				p.Samples++
			}
		}
		sample()
		for {
			select {
			case <-m.stop:
				sample()
				return
			case <-tick.C:
				sample()
			}
		}
	}()
	return m
}

func (m *nodePeakMonitor) Stop() []nodePeak {
	if m == nil {
		return nil
	}
	m.once.Do(func() { close(m.stop) })
	<-m.done
	out := make([]nodePeak, 0, len(m.wanted))
	for _, p := range m.wanted {
		if p.MinAvailableMiB == ^uint64(0) {
			p.MinAvailableMiB = 0
		}
		out = append(out, *p)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].NodeName < out[j].NodeName })
	return out
}

func monitorDistributedLoadN(controller string, baselines map[string]types.GenerativeRelayStatus, targetRemoteBytes uint64, loadDone, processDone <-chan struct{}, renderer *progressRenderer, gate *runtimeOutputGate, phase4 string, session bool, result chan<- loadProgressResult) {
	if targetRemoteBytes == 0 {
		result <- loadProgressResult{}
		return
	}
	start := time.Now()
	lastAt := start
	var lastTotal uint64
	var ema float64
	step := 0
	ticker := time.NewTicker(distributedLoadProgressInterval)
	defer ticker.Stop()
	current := func() (uint64, map[string]uint64) {
		total := uint64(0)
		per := map[string]uint64{}
		for id, b := range baselines {
			st, err := getGenerativeRelayStatus(controller, id)
			if err != nil || st.BytesToWorker < b.BytesToWorker {
				continue
			}
			d := st.BytesToWorker - b.BytesToWorker
			total += d
			per[id] = d
		}
		return total, per
	}
	finish := func() {
		transferred, _ := current()
		dur := time.Since(start)
		avg := float64(transferred) / math.Max(dur.Seconds(), .001)
		renderer.Done(fmt.Sprintf("      Relay traffic to workers: %.2f GiB  avg %s  elapsed %s", float64(transferred)/(1024*1024*1024), formatBytesRate(avg), formatClock(dur)))
		fmt.Fprintf(os.Stderr, "      Remote tensor payload planned: %.2f GiB; relay traffic observed: %.2f GiB\n", float64(targetRemoteBytes)/(1024*1024*1024), float64(transferred)/(1024*1024*1024))
		if transferred > targetRemoteBytes {
			fmt.Fprintln(os.Stderr, "      Relay traffic includes protocol/runtime overhead and is not a tensor-payload byte count.")
		}
		if transferred < targetRemoteBytes {
			avoided := targetRemoteBytes - transferred
			fmt.Fprintf(os.Stderr, "      RPC cache/reuse effect: at least ~%.2f GiB of planned tensor payload avoided relay transfer\n", float64(avoided)/(1024*1024*1024))
		}
		fmt.Fprintf(os.Stderr, "      ✓ Distributed model ready in %s\n\n[4/4] %s\n", formatClock(dur), phase4)
		if session {
			fmt.Fprintln(os.Stderr, "      Send additional prompts normally. Use /exit for a graceful NIBIA session close.")
		}
		if gate != nil {
			gate.Open()
		}
		result <- loadProgressResult{Duration: dur, Transferred: transferred, TargetBytes: targetRemoteBytes, AverageBps: avg}
	}
	for {
		select {
		case <-loadDone:
			finish()
			return
		case <-processDone:
			if gate != nil {
				gate.Open()
			}
			result <- loadProgressResult{Duration: time.Since(start), TargetBytes: targetRemoteBytes}
			return
		case now := <-ticker.C:
			transferred, _ := current()
			delta := uint64(0)
			if transferred >= lastTotal {
				delta = transferred - lastTotal
			}
			secs := now.Sub(lastAt).Seconds()
			inst := float64(delta) / math.Max(secs, .001)
			if ema == 0 {
				ema = inst
			} else {
				ema = .2*inst + .8*ema
			}
			// Persistent RPC cache can make actual relay transfer dramatically
			// smaller than planned remote tensor placement. A percentage or ETA
			// against targetRemoteBytes is therefore not model-load progress.
			renderer.Update(formatCachedDistributedLoadProgress(step, transferred, ema, now.Sub(start)))
			step++
			// Do not declare the model ready from byte traffic alone; wait for
			// llama.cpp's actual interactive/generation boundary observed by
			// loadTracker.
			lastAt = now
			lastTotal = transferred
		}
	}
}

const distributedLoadProgressInterval = 750 * time.Millisecond

var cachedLoadSpinner = []rune{'⠋', '⠙', '⠹', '⠸', '⠼', '⠴', '⠦', '⠧', '⠇', '⠏'}

func formatCachedDistributedLoadProgress(step int, transferred uint64, bytesPerSecond float64, elapsed time.Duration) string {
	return fmt.Sprintf("      %c Loading  transfer %.2f GiB  %s  %s",
		cachedLoadSpinner[step%len(cachedLoadSpinner)],
		float64(transferred)/(1024*1024*1024),
		formatBytesRate(bytesPerSecond),
		formatClock(elapsed))
}

func plannedRemoteTargetBytes(modelBytes int64, plan automaticGenerativePlanN) uint64 {
	var share float64
	for _, d := range plan.Selected {
		if !d.Local {
			share += d.Share
		}
	}
	return uint64(float64(modelBytes) * share)
}

func planNodeCount(plan automaticGenerativePlanN) int { return len(plan.Selected) }

func waitForContext(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

func prepareRemoteFabric(controller string, nodes []types.GenerativeNodeCapability, autoStart bool, basePort int) ([]types.GenerativeRelayStatus, error) {
	relays := make([]types.GenerativeRelayStatus, 0, len(nodes))
	prepared := make([]types.GenerativeNodeCapability, 0, len(nodes))
	for i, n := range nodes {
		if autoStart {
			if _, err := ensureManagedRPCWorker(controller, n); err != nil {
				_ = cleanupRemoteFabric(controller, prepared)
				return nil, fmt.Errorf("prepare RPC worker on %s: %w", n.NodeName, err)
			}
			prepared = append(prepared, n)
			relay, err := ensureGenerativeRelay(controller, n, basePort+i)
			if err != nil {
				_ = cleanupRemoteFabric(controller, prepared)
				return nil, fmt.Errorf("prepare relay for %s: %w", n.NodeName, err)
			}
			relays = append(relays, relay)
		} else {
			st, err := getGenerativeRelayStatus(controller, n.NodeID)
			if err != nil {
				return nil, fmt.Errorf("relay status for %s: %w", n.NodeName, err)
			}
			if !st.Running || st.ReadyTunnelCount < 1 || strings.TrimSpace(st.LocalEndpoint) == "" {
				return nil, fmt.Errorf("relay to %s is not ready", n.NodeName)
			}
			relays = append(relays, st)
		}
	}
	return relays, nil
}

// cleanupRemoteFabric tears down ephemeral compute resources owned by a NIBIA
// workload. Agents remain persistent; llama.cpp RPC workers and controller-side
// loopback relays are intentionally on-demand. Relay teardown happens first so
// loopback ports (55052, 55053, ...) are immediately reusable by the next job.
func cleanupRemoteFabric(controller string, nodes []types.GenerativeNodeCapability) []string {
	warnings := []string{}
	seen := map[string]bool{}
	for i := len(nodes) - 1; i >= 0; i-- {
		n := nodes[i]
		if strings.TrimSpace(n.NodeID) == "" || seen[n.NodeID] {
			continue
		}
		seen[n.NodeID] = true
		var status types.GenerativeRelayStatus
		if err := apiJSON(http.MethodDelete, controller, "/v1/generative/relays/"+n.NodeID, nil, &status); err != nil {
			warnings = append(warnings, fmt.Sprintf("%s relay cleanup failed: %v", n.NodeName, err))
		}
	}
	seen = map[string]bool{}
	for i := len(nodes) - 1; i >= 0; i-- {
		n := nodes[i]
		if strings.TrimSpace(n.NodeID) == "" || seen[n.NodeID] {
			continue
		}
		seen[n.NodeID] = true
		status, err := runRPCWorkerAction(controller, n, executor.TaskLlamaRPCStop)
		if err != nil {
			if !errors.Is(err, errCleanupTargetUnavailable) {
				warnings = append(warnings, fmt.Sprintf("%s RPC worker cleanup failed: %v", n.NodeName, err))
			}
			continue
		}
		if status.Running {
			warnings = append(warnings, fmt.Sprintf("%s RPC worker cleanup returned running=true", n.NodeName))
		}
	}
	return warnings
}

func restartRemoteWorkers(controller string, nodes []types.GenerativeNodeCapability) error {
	var errs []string
	for _, n := range nodes {
		if _, err := restartManagedRPCWorker(controller, n); err != nil {
			errs = append(errs, fmt.Sprintf("%s: %v", n.NodeName, err))
		}
	}
	if len(errs) > 0 {
		return fmt.Errorf("%s", strings.Join(errs, "; "))
	}
	return nil
}

func relayEndpoints(relays []types.GenerativeRelayStatus) string {
	vals := make([]string, 0, len(relays))
	for _, r := range relays {
		if strings.TrimSpace(r.LocalEndpoint) != "" {
			vals = append(vals, r.LocalEndpoint)
		}
	}
	return strings.Join(vals, ",")
}

func allRemoteDevicesVisible(devices []llamaDevice, relays []types.GenerativeRelayStatus) bool {
	seen := map[string]bool{}
	for _, d := range devices {
		if d.Remote {
			seen[strings.TrimSpace(d.Name)] = true
		}
	}
	for _, r := range relays {
		if !seen[strings.TrimSpace(r.LocalEndpoint)] {
			return false
		}
	}
	return len(relays) > 0
}

func printAutomaticPlanN(local types.GenerativeNodeCapability, model string, modelBytes int64, ctx, tokens int, loadMode, lifecycle string, plan automaticGenerativePlanN, verbose bool) {
	fmt.Printf("      Model:          %s\n", cleanModelDisplayName(model))
	fmt.Printf("      Model size:     %.2f GiB (%d MiB)\n", float64(modelBytes)/(1024*1024*1024), plan.ModelMiB)
	fmt.Printf("      Classification: %s\n", plan.Classification)
	fmt.Printf("      Execution:      %s\n", map[bool]string{true: "DISTRIBUTED", false: "LOCAL"}[plan.Distributed])
	fmt.Printf("      Lifecycle:      %s\n", lifecycle)
	fmt.Printf("      Selected nodes: %d\n", len(plan.Selected))
	detailed := verbose || plan.Classification == "INSUFFICIENT-FABRIC-CAPACITY"
	for i, d := range plan.Selected {
		name := d.NodeName
		if d.Local {
			name = local.NodeName
			if name == "" {
				name = "LOCAL"
			}
		}
		execID := d.Device.ID
		if !d.Local {
			execID = fmt.Sprintf("RPC%d", i-1)
		}
		if !detailed {
			fmt.Printf("        %-18s usable=%5.2f GiB  share=%5.1f%%\n", name, float64(d.UsableMiB)/1024, d.Share*100)
		} else if d.Local {
			fmt.Printf("        %-18s %-5s runtime-free=%5d MiB  OS-avail=%5d MiB  basis=%5d MiB  reserve=%4d MiB  usable=%5d MiB  planned share=%5.1f%%\n", name, execID, d.RuntimeFreeMiB, d.OSAvailableMiB, d.CapacityBasisMiB, d.ReserveMiB, d.UsableMiB, d.Share*100)
		} else {
			fmt.Printf("        %-18s %-5s runtime-free=%5d MiB  OS-avail=%5d MiB  basis=%5d MiB  reserve=%4d MiB  usable=%5d MiB  share=%5.1f%%\n", name, execID, d.RuntimeFreeMiB, d.OSAvailableMiB, d.CapacityBasisMiB, d.ReserveMiB, d.UsableMiB, d.Share*100)
		}
	}
	if strings.TrimSpace(plan.ReservePolicySummary) != "" {
		fmt.Printf("      Memory reserve: %s\n", plan.ReservePolicySummary)
	} else {
		fmt.Printf("      Memory reserve: fixed %d MiB\n", plan.ReserveMiB)
	}
	fmt.Printf("      Planned memory: %.2f GiB aggregate node-local capacity\n", float64(plan.AggregateMiB)/1024)
	if tokens > 0 {
		fmt.Printf("      Context/tokens: %d / %d\n", ctx, tokens)
	} else {
		fmt.Printf("      Context:        %d\n", ctx)
	}
	if detailed {
		fmt.Printf("      Load mode:      %s\n", loadMode)
		fmt.Println("      Shared RAM:     no (independent node-local memory domains)")
	}
	fmt.Println("      ✓ Plan ready")
}

// filepathBase exists to keep the N-node planner helpers dependency-light.
func filepathBase(path string) string {
	path = strings.ReplaceAll(path, "\\", "/")
	if i := strings.LastIndex(path, "/"); i >= 0 {
		return path[i+1:]
	}
	return path
}

// cleanModelDisplayName is the product-facing model label. It deliberately
// omits the GGUF file extension and any NIBIA UI alias/branding.
func cleanModelDisplayName(path string) string {
	name := strings.TrimSpace(filepathBase(path))
	if strings.EqualFold(filepath.Ext(name), ".gguf") {
		name = strings.TrimSuffix(name, filepath.Ext(name))
	}
	// OpenAI-compatible clients commonly require model IDs without whitespace.
	// Preserve case, dashes, and quantization underscores while normalizing any
	// accidental filename whitespace to a single dash.
	name = strings.Join(strings.Fields(name), "-")
	return name
}

func remoteTargetSummary(modelBytes int64, plan automaticGenerativePlanN) []string {
	out := []string{}
	var remoteShare float64
	var workers int
	for _, d := range plan.Selected {
		if d.Local {
			continue
		}
		remoteShare += d.Share
		workers++
	}
	if workers > 0 {
		totalTarget := float64(modelBytes) * remoteShare / (1024 * 1024 * 1024)
		out = append(out, fmt.Sprintf("      Remote target total: ~%.2f GiB (%4.1f%% of model across %d worker%s)", totalTarget, remoteShare*100, workers, map[bool]string{true: "", false: "s"}[workers == 1]))
	}
	for _, d := range plan.Selected {
		if d.Local {
			continue
		}
		target := float64(modelBytes) * d.Share / (1024 * 1024 * 1024)
		out = append(out, fmt.Sprintf("        %-18s ~%.2f GiB target (%4.1f%%)", d.NodeName, target, d.Share*100))
	}
	return out
}

func selectedRelayStatusDeltas(controller string, plan automaticGenerativePlanN, baselines map[string]types.GenerativeRelayStatus) []string {
	var lines []string
	for _, d := range plan.Selected {
		if d.Local {
			continue
		}
		b, ok := baselines[d.NodeID]
		if !ok {
			continue
		}
		st, err := getGenerativeRelayStatus(controller, d.NodeID)
		if err != nil {
			continue
		}
		to := uint64(0)
		from := uint64(0)
		if st.BytesToWorker >= b.BytesToWorker {
			to = st.BytesToWorker - b.BytesToWorker
		}
		if st.BytesFromWorker >= b.BytesFromWorker {
			from = st.BytesFromWorker - b.BytesFromWorker
		}
		lines = append(lines, fmt.Sprintf("  %-18s share=%5.1f%%  RPC→ %.2f GiB  RPC← %.2f MiB  bridges=%d failed=%d", d.NodeName, d.Share*100, float64(to)/(1024*1024*1024), float64(from)/(1024*1024), st.SuccessfulBridges-b.SuccessfulBridges, st.FailedBridges-b.FailedBridges))
	}
	return lines
}

func hasZeroMemoryComputeBackend(devices []llamaDevice) bool {
	for _, d := range devices {
		if !d.Remote && d.TotalMiB == 0 && d.FreeMiB == 0 {
			name := strings.ToLower(d.Name)
			if strings.Contains(name, "accelerate") || strings.Contains(name, "blas") {
				return true
			}
		}
	}
	return false
}

func llamaVersionLine(path string) string {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, path, "--version").CombinedOutput()
	if err != nil && len(out) == 0 {
		return ""
	}
	for _, line := range strings.Split(string(out), "\n") {
		line = strings.TrimSpace(line)
		if line != "" {
			return line
		}
	}
	return ""
}

type llamaRuntimeIdentity struct {
	Version string
	Commit  string
}

var llamaRuntimeVersionRE = regexp.MustCompile(`(?i)version:\s*([^\s(]+)`)
var llamaRuntimeCommitRE = regexp.MustCompile(`(?i)commit\s+([0-9a-f]+)`)

func parseLlamaRuntimeIdentity(raw string) llamaRuntimeIdentity {
	raw = strings.TrimSpace(raw)
	id := llamaRuntimeIdentity{}
	if m := llamaRuntimeVersionRE.FindStringSubmatch(raw); len(m) == 2 {
		id.Version = strings.ToLower(strings.TrimSpace(m[1]))
	}
	if m := llamaRuntimeCommitRE.FindStringSubmatch(raw); len(m) == 2 {
		id.Commit = strings.ToLower(strings.TrimSpace(m[1]))
	}
	return id
}

func llamaRuntimeCompatible(a, b string) bool {
	a = strings.TrimSpace(a)
	b = strings.TrimSpace(b)
	if a == "" || b == "" {
		return true
	}
	ia := parseLlamaRuntimeIdentity(a)
	ib := parseLlamaRuntimeIdentity(b)
	if ia.Version == "" || ib.Version == "" || ia.Commit == "" || ib.Commit == "" {
		return a == b
	}
	if ia.Version != ib.Version {
		return false
	}
	// llama.cpp builds may report the same Git commit at different short-hash
	// lengths (for example df03399 vs df03399b8) and with different local
	// build counters. Treat those as the same runtime source identity.
	if len(ia.Commit) < 7 || len(ib.Commit) < 7 {
		return ia.Commit == ib.Commit
	}
	return strings.HasPrefix(ia.Commit, ib.Commit) || strings.HasPrefix(ib.Commit, ia.Commit)
}

func validateRuntimeParity(localVersion string, nodes []types.GenerativeNodeCapability) error {
	localVersion = strings.TrimSpace(localVersion)
	if localVersion == "" {
		return nil
	}
	var mismatches []string
	for _, n := range nodes {
		v := strings.TrimSpace(n.LlamaCPPVersion)
		if v == "" {
			continue
		}
		if !llamaRuntimeCompatible(localVersion, v) {
			mismatches = append(mismatches, fmt.Sprintf("%s=%q", n.NodeName, v))
		}
	}
	if len(mismatches) > 0 {
		return fmt.Errorf("llama.cpp runtime mismatch: coordinator=%q; %s", localVersion, strings.Join(mismatches, "; "))
	}
	return nil
}
