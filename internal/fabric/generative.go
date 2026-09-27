package fabric

import (
	"strings"

	"github.com/nibia-ai/fabric/internal/types"
)

// GenerativeSummary reports the current Fabric capacity and llama.cpp
// coordinator/worker readiness used by distributed generative execution.
// Memory totals remain node-local; this is a capacity view, not a shared
// address-space claim.
func GenerativeSummary(nodes []types.NodeStatus) types.GenerativeFabricSummary {
	out := types.GenerativeFabricSummary{
		KnownNodes:         len(nodes),
		SharedAddressSpace: false,
	}

	coordinators := map[string]bool{}
	workers := map[string]bool{}

	for _, status := range nodes {
		online := !strings.EqualFold(strings.TrimSpace(status.State), "OFFLINE")
		if online {
			out.OnlineNodes++
		}

		inv, ok := llamaRuntime(status.Node.Runtimes)
		if !ok || !inv.Installed {
			continue
		}
		out.LlamaCPPInstalledNodes++
		if online {
			out.AggregateTotalMemoryMB += status.Node.MemoryTotalMB
			out.AggregateAvailableMemoryMB += status.Node.MemoryAvailableMB
		}

		coord := hasFeature(inv.Features, "rpc-coordinator")
		worker := hasFeature(inv.Features, "rpc-worker")
		if online && coord {
			out.DistributedCoordinatorNodes++
			coordinators[status.Node.ID] = true
		}
		if online && worker {
			out.DistributedWorkerNodes++
			workers[status.Node.ID] = true
		}

		out.Nodes = append(out.Nodes, types.GenerativeNodeCapability{
			NodeID:                      status.Node.ID,
			NodeName:                    status.Node.Name,
			Hostname:                    status.Node.Hostname,
			NodeState:                   strings.ToUpper(strings.TrimSpace(status.State)),
			ResourceState:               strings.ToUpper(strings.TrimSpace(status.Node.ResourceState)),
			OS:                          status.Node.OS,
			Arch:                        status.Node.Arch,
			Platform:                    status.Node.Platform,
			CPUModel:                    status.Node.CPUModel,
			CPUUsedPct:                  status.Node.CPUUsedPct,
			MemoryUsedPct:               status.Node.MemoryUsedPct,
			MemoryHeadroomPct:           status.Node.MemoryHeadroomPct,
			MemoryPressureLevel:         status.Node.MemoryPressureLevel,
			PreferredAddress:            status.Node.PreferredAddress,
			NetworkInterfaces:           append([]types.NetworkInterfaceInventory(nil), status.Node.NetworkInterfaces...),
			AgentVersion:                status.Node.AgentVersion,
			MemoryTotalMB:               status.Node.MemoryTotalMB,
			MemoryAvailableMB:           status.Node.MemoryAvailableMB,
			PhysicalCores:               status.Node.CPUPhysical,
			LogicalCores:                status.Node.CPULogical,
			LlamaCPPVersion:             inv.Version,
			LlamaCPPFeatures:            append([]string(nil), inv.Features...),
			CoordinatorCapable:          coord,
			WorkerCapable:               worker,
			LastSeen:                    status.LastSeen,
			TelemetryUpdatedAt:          status.Node.UpdatedAt,
			RPCWorkerManaged:            status.Node.RPCWorkerManaged,
			RPCWorkerRunning:            status.Node.RPCWorkerRunning,
			RPCWorkerPID:                status.Node.RPCWorkerPID,
			RPCWorkerProcessCPUPercent:  status.Node.RPCWorkerProcessCPUPercent,
			RPCWorkerProcessRSSBytes:    status.Node.RPCWorkerProcessRSSBytes,
			RPCWorkerPeakCPUPercent:     status.Node.RPCWorkerPeakCPUPercent,
			RPCWorkerPeakRSSBytes:       status.Node.RPCWorkerPeakRSSBytes,
			RPCWorkerTelemetryStartedAt: status.Node.RPCWorkerTelemetryStartedAt,
		})
	}

	for coordID := range coordinators {
		for workerID := range workers {
			if coordID != workerID {
				out.DistributedGenerativeReady = true
				return out
			}
		}
	}
	return out
}

func llamaRuntime(runtimes []types.RuntimeInventory) (types.RuntimeInventory, bool) {
	for _, inv := range runtimes {
		if strings.EqualFold(strings.TrimSpace(inv.Name), "llama.cpp") {
			return inv, true
		}
	}
	return types.RuntimeInventory{}, false
}

func hasFeature(features []string, target string) bool {
	for _, feature := range features {
		if strings.EqualFold(strings.TrimSpace(feature), strings.TrimSpace(target)) {
			return true
		}
	}
	return false
}
