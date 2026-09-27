package executor

import (
	"context"
	"testing"

	"github.com/nibia-ai/fabric/internal/types"
)

func TestLlamaCPPCapabilitiesFromRuntimeFeatures(t *testing.T) {
	caps := CapabilitiesFromAIInventory([]types.RuntimeInventory{{
		Name: "llama.cpp", Installed: true, Reachable: true,
		Features: []string{"cli", "rpc-coordinator", "rpc-worker"},
	}})
	want := map[string]bool{
		"runtime:llama.cpp":        true,
		"llamacpp:cli":             true,
		"llamacpp:rpc-coordinator": true,
		"llamacpp:rpc-worker":      true,
	}
	for _, cap := range caps {
		delete(want, cap)
	}
	if len(want) != 0 {
		t.Fatalf("missing llama.cpp capabilities: %v; got=%v", want, caps)
	}
}

func TestNormalizeLlamaRPCWorkerLifecycle(t *testing.T) {
	start := types.WorkloadSpec{Type: "rpc-start", RPCPort: 50053, RPCCache: true}
	if err := NormalizeWorkload(&start); err != nil {
		t.Fatal(err)
	}
	if start.Type != TaskLlamaRPCStart || start.RPCPort != 50053 || !start.RPCCache {
		t.Fatalf("unexpected start normalization: %+v", start)
	}
	caps := RequiredCapabilities(start)
	want := []string{"executor-v1", "task-llamacpp-rpc-worker", "runtime:llama.cpp", "llamacpp:rpc-worker"}
	if len(caps) != len(want) {
		t.Fatalf("caps=%v want=%v", caps, want)
	}
	for i := range want {
		if caps[i] != want[i] {
			t.Fatalf("caps[%d]=%q want=%q", i, caps[i], want[i])
		}
	}

	status := types.WorkloadSpec{Type: "rpc-status", RPCPort: 60000, RPCCache: true}
	if err := NormalizeWorkload(&status); err != nil {
		t.Fatal(err)
	}
	if status.Type != TaskLlamaRPCStatus || status.RPCPort != 0 || status.RPCCache {
		t.Fatalf("status should not retain start-only fields: %+v", status)
	}

	stop := types.WorkloadSpec{Type: "rpc-stop"}
	if err := NormalizeWorkload(&stop); err != nil {
		t.Fatal(err)
	}
	if stop.Type != TaskLlamaRPCStop {
		t.Fatalf("stop type=%q", stop.Type)
	}
}

func TestNormalizeLlamaRPCWorkerRejectsInvalidPort(t *testing.T) {
	w := types.WorkloadSpec{Type: "rpc-start", RPCPort: 80}
	if err := NormalizeWorkload(&w); err == nil {
		t.Fatal("expected invalid RPC worker port to be rejected")
	}
}

func TestNormalizePowerGuardLifecycle(t *testing.T) {
	acquire := types.WorkloadSpec{Type: "power-guard-acquire"}
	if err := NormalizeWorkload(&acquire); err != nil {
		t.Fatal(err)
	}
	if acquire.Type != TaskPowerGuardAcquire {
		t.Fatalf("acquire type=%q", acquire.Type)
	}
	caps := RequiredCapabilities(acquire)
	want := []string{"executor-v1", "task-power-guard"}
	if len(caps) != len(want) {
		t.Fatalf("caps=%v want=%v", caps, want)
	}
	for i := range want {
		if caps[i] != want[i] {
			t.Fatalf("caps[%d]=%q want=%q", i, caps[i], want[i])
		}
	}

	status := types.WorkloadSpec{Type: "power-guard-status"}
	if err := NormalizeWorkload(&status); err != nil {
		t.Fatal(err)
	}
	if status.Type != TaskPowerGuardStatus {
		t.Fatalf("status type=%q", status.Type)
	}

	release := types.WorkloadSpec{Type: "power-guard-release"}
	if err := NormalizeWorkload(&release); err != nil {
		t.Fatal(err)
	}
	if release.Type != TaskPowerGuardRelease {
		t.Fatalf("release type=%q", release.Type)
	}
}

func TestLegacyWorkloadsAreRejected(t *testing.T) {
	for _, typ := range []string{"ollama", "infer", "embed", "hash-shard", "text-batch", "probe", "cpu", "hold", "host", "runtimes"} {
		w := types.WorkloadSpec{Type: typ}
		if err := NormalizeWorkload(&w); err == nil {
			t.Fatalf("legacy workload %q should be rejected", typ)
		}
	}
}

func TestExecuteRejectsUnsupportedWorkload(t *testing.T) {
	if _, _, err := Execute(context.Background(), types.WorkloadSpec{Type: "rag.ask"}); err == nil {
		t.Fatal("unsupported workload should be rejected")
	}
}
