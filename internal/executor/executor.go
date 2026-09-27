package executor

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/nibia-ai/fabric/internal/types"
)

const (
	TaskLlamaRPCStart          = "llamacpp.rpc.start"
	TaskLlamaRPCStatus         = "llamacpp.rpc.status"
	TaskLlamaRPCStop           = "llamacpp.rpc.stop"
	TaskLlamaRPCTelemetryReset = "llamacpp.rpc.telemetry.reset"

	TaskPowerGuardAcquire = "power.guard.acquire"
	TaskPowerGuardStatus  = "power.guard.status"
	TaskPowerGuardRelease = "power.guard.release"
)

// NormalizeWorkload accepts only workloads that are part of the public
// NIBIA Fabric v0.7.0-alpha distributed-inference lifecycle. Generic synthetic,
// Ollama, embedding, distributed-text/hash, and RAG workloads are deliberately
// not part of this release surface.
func NormalizeWorkload(w *types.WorkloadSpec) error {
	switch strings.ToLower(strings.TrimSpace(w.Type)) {
	case "llama-rpc-start", "rpc-start", TaskLlamaRPCStart:
		port, cache := w.RPCPort, w.RPCCache
		*w = types.WorkloadSpec{Type: TaskLlamaRPCStart, RPCPort: port, RPCCache: cache}
		if w.RPCPort == 0 {
			w.RPCPort = defaultLlamaRPCPort
		}
		if _, err := normalizeRPCPort(w.RPCPort); err != nil {
			return err
		}

	case "llama-rpc-status", "rpc-status", TaskLlamaRPCStatus:
		*w = types.WorkloadSpec{Type: TaskLlamaRPCStatus}

	case "llama-rpc-stop", "rpc-stop", TaskLlamaRPCStop:
		*w = types.WorkloadSpec{Type: TaskLlamaRPCStop}

	case "llama-rpc-telemetry-reset", "rpc-telemetry-reset", TaskLlamaRPCTelemetryReset:
		*w = types.WorkloadSpec{Type: TaskLlamaRPCTelemetryReset}

	case "power-guard-acquire", TaskPowerGuardAcquire:
		*w = types.WorkloadSpec{Type: TaskPowerGuardAcquire}

	case "power-guard-status", TaskPowerGuardStatus:
		*w = types.WorkloadSpec{Type: TaskPowerGuardStatus}

	case "power-guard-release", TaskPowerGuardRelease:
		*w = types.WorkloadSpec{Type: TaskPowerGuardRelease}

	default:
		return fmt.Errorf("unsupported NIBIA Fabric workload %q", w.Type)
	}
	return nil
}

func RequiredCapabilities(w types.WorkloadSpec) []string {
	switch w.Type {
	case TaskLlamaRPCStart, TaskLlamaRPCStatus, TaskLlamaRPCStop, TaskLlamaRPCTelemetryReset:
		return []string{
			"executor-v1",
			"task-llamacpp-rpc-worker",
			"runtime:llama.cpp",
			"llamacpp:rpc-worker",
		}
	case TaskPowerGuardAcquire, TaskPowerGuardStatus, TaskPowerGuardRelease:
		return []string{"executor-v1", "task-power-guard"}
	default:
		return nil
	}
}

// DynamicCapabilities returns runtime-derived facts that may change while the
// Agent remains running. v0.7.0-alpha inventories only the managed/external
// llama.cpp runtime used by NIBIA Fabric.
func DynamicCapabilities(ctx context.Context) []string {
	runtimes := CollectAIInventory(ctx)
	return CapabilitiesFromAIInventory(runtimes)
}

func CapabilitiesFromAIInventory(runtimes []types.RuntimeInventory) []string {
	var caps []string
	for _, runtime := range runtimes {
		if runtime.Installed {
			caps = append(caps, "runtime:"+strings.ToLower(strings.TrimSpace(runtime.Name)))
		}
		if strings.EqualFold(runtime.Name, "llama.cpp") {
			for _, feature := range runtime.Features {
				feature = strings.ToLower(strings.TrimSpace(feature))
				if feature != "" {
					caps = append(caps, "llamacpp:"+feature)
				}
			}
		}
	}
	sort.Strings(caps)
	return uniqueStrings(caps)
}

// CollectAIInventory reports only llama.cpp, the runtime actually used by this
// release. Model files are selected explicitly by the Primary when running or
// serving and are therefore not inventoried from unrelated local runtimes.
func CollectAIInventory(ctx context.Context) []types.RuntimeInventory {
	var runtimes []types.RuntimeInventory
	if llamaRuntime, ok := collectLlamaCPPInventory(ctx); ok {
		runtimes = append(runtimes, llamaRuntime)
	}
	return runtimes
}

func Execute(_ context.Context, w types.WorkloadSpec) (string, *types.ExecutorResult, error) {
	switch w.Type {
	case TaskLlamaRPCStart:
		status, err := StartManagedLlamaRPC(w.RPCPort, w.RPCCache)
		if err != nil {
			return "", nil, err
		}
		return fmt.Sprintf("llama.cpp RPC worker started on %s pid=%d", status.Endpoint, status.PID),
			&types.ExecutorResult{Kind: TaskLlamaRPCStart, Data: status}, nil

	case TaskLlamaRPCStatus:
		status := ManagedLlamaRPCStatus()
		return fmt.Sprintf("llama.cpp RPC worker running=%t endpoint=%s", status.Running, status.Endpoint),
			&types.ExecutorResult{Kind: TaskLlamaRPCStatus, Data: status}, nil

	case TaskLlamaRPCStop:
		before := ManagedLlamaRPCStatus()
		if err := StopManagedLlamaRPC(); err != nil {
			return "", nil, err
		}
		after := ManagedLlamaRPCStatus()
		return fmt.Sprintf("llama.cpp RPC worker stopped (was_running=%t)", before.Running),
			&types.ExecutorResult{Kind: TaskLlamaRPCStop, Data: after}, nil

	case TaskLlamaRPCTelemetryReset:
		status := ResetManagedLlamaRPCTelemetry()
		return fmt.Sprintf("llama.cpp RPC worker telemetry reset pid=%d", status.PID),
			&types.ExecutorResult{Kind: TaskLlamaRPCTelemetryReset, Data: status}, nil

	case TaskPowerGuardAcquire:
		status, err := AcquireManagedPowerGuard("active NIBIA generative workload")
		if err != nil {
			return "", &types.ExecutorResult{Kind: TaskPowerGuardAcquire, Data: status}, err
		}
		return fmt.Sprintf("power guard active backend=%s", status.Backend),
			&types.ExecutorResult{Kind: TaskPowerGuardAcquire, Data: status}, nil

	case TaskPowerGuardStatus:
		status := ManagedPowerGuardStatus()
		return fmt.Sprintf("power guard active=%t backend=%s", status.Active, status.Backend),
			&types.ExecutorResult{Kind: TaskPowerGuardStatus, Data: status}, nil

	case TaskPowerGuardRelease:
		status, err := ReleaseManagedPowerGuard()
		if err != nil {
			return "", &types.ExecutorResult{Kind: TaskPowerGuardRelease, Data: status}, err
		}
		return "power guard released",
			&types.ExecutorResult{Kind: TaskPowerGuardRelease, Data: status}, nil

	default:
		return "", nil, fmt.Errorf("executor refuses unsupported workload %q", w.Type)
	}
}

func uniqueStrings(values []string) []string {
	if len(values) == 0 {
		return nil
	}
	out := make([]string, 0, len(values))
	var last string
	for _, v := range values {
		if v == "" || v == last {
			continue
		}
		out = append(out, v)
		last = v
	}
	return out
}
