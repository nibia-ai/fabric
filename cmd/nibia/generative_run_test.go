package main

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/nibia-ai/fabric/internal/executor"
	"github.com/nibia-ai/fabric/internal/types"
)

func TestParseLlamaDevices(t *testing.T) {
	text := `Available devices:
  MTL0: Apple M2 Pro (10922 MiB, 10922 MiB free)
  BLAS: Accelerate (0 MiB, 0 MiB free)
  RPC0: 127.0.0.1:55052 (5784 MiB, 5784 MiB free)
`
	got := parseLlamaDevices(text)
	want := []llamaDevice{
		{ID: "MTL0", Name: "Apple M2 Pro", TotalMiB: 10922, FreeMiB: 10922},
		{ID: "BLAS", Name: "Accelerate", TotalMiB: 0, FreeMiB: 0},
		{ID: "RPC0", Name: "127.0.0.1:55052", TotalMiB: 5784, FreeMiB: 5784, Remote: true},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("devices mismatch:\n got=%+v\nwant=%+v", got, want)
	}
}

func TestAutomaticPlanCapacityExpansion(t *testing.T) {
	devices := []llamaDevice{
		{ID: "MTL0", Name: "Apple M2 Pro", FreeMiB: 10922},
		{ID: "RPC0", Name: "127.0.0.1:55052", FreeMiB: 5784, Remote: true},
	}
	plan, err := planAutomaticGenerativeRun(12100000000, 512, false, devices)
	if err != nil {
		t.Fatal(err)
	}
	if plan.Classification != "CAPACITY-EXPANDING" || !plan.Distributed {
		t.Fatalf("unexpected plan: %+v", plan)
	}
	if plan.LocalShare < 0.65 || plan.LocalShare > 0.66 {
		t.Fatalf("unexpected local share %.4f", plan.LocalShare)
	}
	if plan.RemoteShare < 0.34 || plan.RemoteShare > 0.35 {
		t.Fatalf("unexpected remote share %.4f", plan.RemoteShare)
	}
}

func TestAutomaticPlanFitsLocal(t *testing.T) {
	devices := []llamaDevice{
		{ID: "MTL0", Name: "Apple M2 Pro", FreeMiB: 10922},
		{ID: "RPC0", Name: "127.0.0.1:55052", FreeMiB: 5784, Remote: true},
	}
	plan, err := planAutomaticGenerativeRun(5*1024*1024*1024, 512, false, devices)
	if err != nil {
		t.Fatal(err)
	}
	if plan.Classification != "FITS-LOCAL" || plan.Distributed {
		t.Fatalf("unexpected plan: %+v", plan)
	}
}

func TestBuildAutomaticDistributedLlamaArgs(t *testing.T) {
	plan := automaticGenerativePlan{
		Distributed: true,
		Local:       llamaDevice{ID: "MTL0", FreeMiB: 10922},
		Remote:      llamaDevice{ID: "RPC0", FreeMiB: 5784, Remote: true},
	}
	got := buildAutomaticLlamaArgs(
		"/tmp/model.gguf", "hello", 32, 1024, "none", "127.0.0.1:55052", plan, false,
	)
	want := []string{
		"--simple-io",
		"--rpc", "127.0.0.1:55052",
		"-m", "/tmp/model.gguf",
		"-p", "hello",
		"-n", "32",
		"-c", "1024",
		"-dev", "MTL0,RPC0",
		"-sm", "layer",
		"-ts", "10922,5784",
		"-ngl", "all",
		"-fit", "off",
		"--load-mode", "none",
		"-st",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("args mismatch:\n got=%q\nwant=%q", got, want)
	}
}

func TestBuildAutomaticDistributedLlamaArgsSession(t *testing.T) {
	plan := automaticGenerativePlan{
		Distributed: true,
		Local:       llamaDevice{ID: "MTL0", FreeMiB: 10922},
		Remote:      llamaDevice{ID: "RPC0", FreeMiB: 5784, Remote: true},
	}
	got := buildAutomaticLlamaArgs(
		"/tmp/model.gguf", "hello", 32, 1024, "none", "127.0.0.1:55052", plan, true,
	)
	for _, arg := range got {
		if arg == "-st" || arg == "--single-turn" {
			t.Fatalf("session mode must not contain single-turn flag: %q", got)
		}
	}
}

func TestRenderProgressBar(t *testing.T) {
	if got := renderProgressBar(50, 10); got != "█████░░░░░" {
		t.Fatalf("unexpected progress bar %q", got)
	}
	if got := renderProgressBar(100, 4); got != "████" {
		t.Fatalf("unexpected full progress bar %q", got)
	}
}

func TestLoadTrackerWaitsForRuntimeReadyBoundary(t *testing.T) {
	tracker := newLoadTracker()
	tracker.observe([]byte("\x1b[32mbuild      : b1-df03399\x1b[0m\nmodalities : text\n"))
	if tracker.Ready() {
		t.Fatal("startup banner must not mark the runtime ready")
	}
	tracker.observe([]byte("\n> Reply exactly: hello\n"))
	if !tracker.Ready() {
		t.Fatal("expected interactive prompt boundary to mark runtime ready")
	}
}

func TestRuntimeOutputGateSuppressesStartupAndFlushesGeneratedTextAfterOpen(t *testing.T) {
	tracker := newLoadTracker()
	var raw strings.Builder
	renderer := newProgressRenderer(&raw)
	gate := newRuntimeOutputGate(tracker, renderer, false)
	var dst strings.Builder

	_, _ = gate.Write(&dst, []byte("build      : b1\nmodalities : text\n"), true)
	_, _ = gate.Write(&dst, []byte("\n> prompt\n"), false)
	_, _ = gate.Write(&dst, []byte("generated token"), false)
	if dst.Len() != 0 {
		t.Fatalf("runtime output must remain gated before phase 4, got %q", dst.String())
	}
	gate.Open()
	if got := dst.String(); got != "generated token" {
		t.Fatalf("expected only post-ready generated text after gate open, got %q", got)
	}
}

func TestLoadTrackerRecognizesPromptWithLeadingWhitespaceAndAsterisks(t *testing.T) {
	tracker := newLoadTracker()
	tracker.observe([]byte("startup\n   **> Reply exactly: hello\n"))
	if !tracker.Ready() {
		t.Fatal("expected decorated interactive prompt to mark runtime ready")
	}
}

func TestProgressRendererUpdatePrintsLineOnce(t *testing.T) {
	var out strings.Builder
	r := newProgressRenderer(&out)
	r.Update("progress")
	if got := out.String(); got != "progress" {
		t.Fatalf("progress renderer must print one copy, got %q", got)
	}
}

func TestRuntimeOutputGateSuppressesDelayedStderrBannerAfterOpen(t *testing.T) {
	tracker := newLoadTracker()
	var raw strings.Builder
	renderer := newProgressRenderer(&raw)
	gate := newRuntimeOutputGate(tracker, renderer, false)
	var stdout, stderr strings.Builder

	_, _ = gate.Write(&stdout, []byte("\n> prompt\n"), false)
	gate.Open()
	_, _ = gate.Write(&stderr, []byte("build      : b1\nmodalities : text\n"), true)
	_, _ = gate.Write(&stdout, []byte("generated token"), false)
	if stderr.Len() != 0 {
		t.Fatalf("delayed startup stderr must be suppressed, got %q", stderr.String())
	}
	if got := stdout.String(); got != "generated token" {
		t.Fatalf("expected generated stdout, got %q", got)
	}
}

func TestOutputLooksLikeRPCDeviceList(t *testing.T) {
	if !outputLooksLikeRPCDeviceList("RPC0: 127.0.0.1:55052 (5784 MiB, 5784 MiB free)") {
		t.Fatal("expected RPC device output to be recognized")
	}
	if outputLooksLikeRPCDeviceList("MTL0: Apple M2 Pro (10922 MiB, 10922 MiB free)") {
		t.Fatal("local-only output must not be recognized as RPC")
	}
}

func TestAutomaticNPlanSelectsMinimumSufficientRemoteSubset(t *testing.T) {
	devices := []llamaDevice{
		{ID: "MTL0", Name: "Apple M2 Pro", FreeMiB: 10922},
		{ID: "RPC0", Name: "127.0.0.1:55052", FreeMiB: 5784, Remote: true},
		{ID: "RPC1", Name: "127.0.0.1:55053", FreeMiB: 14000, Remote: true},
	}
	remotes := []remoteGenerativeCandidate{
		{Node: types.GenerativeNodeCapability{NodeID: "linux", NodeName: "Worker-Linux"}, Relay: types.GenerativeRelayStatus{LocalEndpoint: "127.0.0.1:55052"}, Device: devices[1]},
		{Node: types.GenerativeNodeCapability{NodeID: "win", NodeName: "Worker-Windows"}, Relay: types.GenerativeRelayStatus{LocalEndpoint: "127.0.0.1:55053"}, Device: devices[2]},
	}
	// ~20 GiB: local alone is insufficient, local + Windows is sufficient,
	// so the smaller Linux worker should not be selected.
	plan, err := planAutomaticGenerativeRunN(20*1024*1024*1024, 512, false, types.GenerativeNodeCapability{}, devices, remotes)
	if err != nil {
		t.Fatal(err)
	}
	if plan.Classification != "CAPACITY-EXPANDING" || !plan.Distributed {
		t.Fatalf("unexpected plan: %+v", plan)
	}
	if len(plan.Selected) != 2 {
		t.Fatalf("expected 2 selected devices, got %d: %+v", len(plan.Selected), plan.Selected)
	}
	if plan.Selected[1].NodeName != "Worker-Windows" {
		t.Fatalf("expected Worker-Windows to be selected, got %+v", plan.Selected)
	}
}

func TestAutomaticNPlanUsesThreeNodesWhenRequired(t *testing.T) {
	devices := []llamaDevice{
		{ID: "MTL0", Name: "Apple M2 Pro", FreeMiB: 10922},
		{ID: "RPC0", Name: "127.0.0.1:55052", FreeMiB: 5784, Remote: true},
		{ID: "RPC1", Name: "127.0.0.1:55053", FreeMiB: 14000, Remote: true},
	}
	remotes := []remoteGenerativeCandidate{
		{Node: types.GenerativeNodeCapability{NodeID: "linux", NodeName: "Worker-Linux"}, Relay: types.GenerativeRelayStatus{LocalEndpoint: "127.0.0.1:55052"}, Device: devices[1]},
		{Node: types.GenerativeNodeCapability{NodeID: "win", NodeName: "Worker-Windows"}, Relay: types.GenerativeRelayStatus{LocalEndpoint: "127.0.0.1:55053"}, Device: devices[2]},
	}
	// ~27 GiB requires local + both remotes after reserves.
	plan, err := planAutomaticGenerativeRunN(27*1024*1024*1024, 512, false, types.GenerativeNodeCapability{}, devices, remotes)
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Selected) != 3 {
		t.Fatalf("expected 3 selected devices, got %d", len(plan.Selected))
	}
	if plan.Classification != "CAPACITY-EXPANDING" {
		t.Fatalf("unexpected classification %s", plan.Classification)
	}
}

func TestBuildAutomaticNDistributedArgs(t *testing.T) {
	plan := automaticGenerativePlanN{Distributed: true, Selected: []plannedGenerativeDevice{
		{Device: llamaDevice{ID: "MTL0"}, UsableMiB: 10410, Local: true},
		{NodeID: "linux", NodeName: "Worker-Linux", RelayEndpoint: "127.0.0.1:55052", UsableMiB: 5272},
		{NodeID: "win", NodeName: "Worker-Windows", RelayEndpoint: "127.0.0.1:55053", UsableMiB: 13000},
	}}
	got := buildAutomaticLlamaArgsN("/tmp/model.gguf", "hello", 32, 1024, "none", plan, false)
	wantFragments := []string{"--simple-io", "--no-display-prompt", "127.0.0.1:55052,127.0.0.1:55053", "MTL0,RPC0,RPC1", "10410,5272,13000", "-st"}
	joined := strings.Join(got, " ")
	for _, f := range wantFragments {
		if !strings.Contains(joined, f) {
			t.Fatalf("missing %q in args %q", f, got)
		}
	}
}

func TestBuildAutomaticNSessionWithoutInitialPromptOmitsPromptFlag(t *testing.T) {
	plan := automaticGenerativePlanN{Selected: []plannedGenerativeDevice{{
		Device: llamaDevice{ID: "CPU0", Name: "System CPU / RAM", CPUOnly: true},
		Local:  true,
	}}}
	got := buildAutomaticLlamaArgsN("/tmp/model.gguf", "", 64, 4096, "none", plan, true)
	for i, arg := range got {
		if arg == "-p" {
			t.Fatalf("interactive session without an initial prompt must omit -p: %q (index %d)", got, i)
		}
	}
	if slices.Contains(got, "-st") {
		t.Fatalf("interactive session must not include -st: %q", got)
	}
	if slices.Contains(got, "--no-display-prompt") {
		t.Fatalf("interactive session should preserve llama-cli prompt display behavior: %q", got)
	}
}

func TestBuildAutomaticLlamaServerArgsN(t *testing.T) {
	plan := automaticGenerativePlanN{Distributed: true, Selected: []plannedGenerativeDevice{
		{Device: llamaDevice{ID: "MTL0"}, UsableMiB: 9560, Local: true},
		{NodeID: "win", NodeName: "Worker-Windows", RelayEndpoint: "127.0.0.1:55053", UsableMiB: 7681},
		{NodeID: "linux", NodeName: "Worker-Linux", RelayEndpoint: "127.0.0.1:55052", UsableMiB: 4099},
	}}
	got := buildAutomaticLlamaServerArgsN("/tmp/model.gguf", "Model-ID", 4096, "none", "127.0.0.1", 8081, plan)
	joined := strings.Join(got, " ")
	for _, f := range []string{"--rpc 127.0.0.1:55053,127.0.0.1:55052", "-dev MTL0,RPC0,RPC1", "-sm layer", "-ts 9560,7681,4099", "--host 127.0.0.1", "--port 8081", "--load-mode none", "-a Model-ID"} {
		if !strings.Contains(joined, f) {
			t.Fatalf("missing %q in args %q", f, got)
		}
	}
}

func TestRuntimeOutputGateIgnoresStderrForReadiness(t *testing.T) {
	tracker := newLoadTracker()
	var screen bytes.Buffer
	renderer := newProgressRenderer(&screen)
	gate := newRuntimeOutputGate(tracker, renderer, false)

	var stderr bytes.Buffer
	if _, err := gate.Write(&stderr, []byte("\n> prompt echoed on stderr\n"), true); err != nil {
		t.Fatal(err)
	}
	if tracker.Ready() {
		t.Fatal("stderr must not trip the runtime-ready boundary")
	}

	var stdout bytes.Buffer
	if _, err := gate.Write(&stdout, []byte("\n> real stdout prompt\n"), false); err != nil {
		t.Fatal(err)
	}
	if !tracker.Ready() {
		t.Fatal("stdout interactive prompt should trip runtime-ready boundary")
	}
}

func TestRuntimeOutputGateSuppressesDelayedStderrStartupAfterReady(t *testing.T) {
	tracker := newLoadTracker()
	var screen bytes.Buffer
	renderer := newProgressRenderer(&screen)
	gate := newRuntimeOutputGate(tracker, renderer, false)
	var stdout, stderr bytes.Buffer

	if _, err := gate.Write(&stdout, []byte("\n> prompt\n"), false); err != nil {
		t.Fatal(err)
	}
	gate.Open()
	if _, err := gate.Write(&stderr, []byte("build : b1-df03399\nmodel : test.gguf\navailable commands:\n"), true); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(stderr.String(), "build") || strings.Contains(screen.String(), "build") {
		t.Fatalf("delayed startup stderr leaked after ready: stderr=%q screen=%q", stderr.String(), screen.String())
	}
}

func TestLlamaRuntimeCompatibleIgnoresBuildCounterAndShortHashLength(t *testing.T) {
	mac := "version: 0.4.0-dev (build 1, commit df03399)"
	windows := "version: 0.4.0-dev (build 10902, commit df03399b8)"
	if !llamaRuntimeCompatible(mac, windows) {
		t.Fatalf("expected same version+commit source identity to be compatible: %q vs %q", mac, windows)
	}
}

func TestLlamaRuntimeCompatibleRejectsDifferentCommit(t *testing.T) {
	a := "version: 0.4.0-dev (build 1, commit df03399)"
	b := "version: 0.4.0-dev (build 10903, commit abcdef12)"
	if llamaRuntimeCompatible(a, b) {
		t.Fatalf("expected different commits to be incompatible: %q vs %q", a, b)
	}
}

func TestLlamaRuntimeCompatibleRejectsDifferentVersion(t *testing.T) {
	a := "version: 0.4.0-dev (build 1, commit df03399)"
	b := "version: 0.5.0-dev (build 1, commit df03399b8)"
	if llamaRuntimeCompatible(a, b) {
		t.Fatalf("expected different versions to be incompatible: %q vs %q", a, b)
	}
}

func TestRemoteTargetSummaryShowsAggregateAndPerWorkerTargets(t *testing.T) {
	plan := automaticGenerativePlanN{Selected: []plannedGenerativeDevice{
		{NodeName: "Primary-Mac", Local: true, Share: 0.632},
		{NodeName: "Worker-Linux", Share: 0.237},
		{NodeName: "Worker-Windows", Share: 0.131},
	}}
	lines := remoteTargetSummary(11561*1024*1024, plan)
	joined := strings.Join(lines, "\n")
	for _, want := range []string{"Remote target total:", "across 2 workers", "Worker-Linux", "Worker-Windows"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("expected %q in remote target summary:\n%s", want, joined)
		}
	}
}

func TestAutomaticNPlanClampsRemoteCapacityToOSAvailable(t *testing.T) {
	devices := []llamaDevice{
		{ID: "MTL0", Name: "Apple M2 Pro", FreeMiB: 10922},
		{ID: "RPC0", Name: "127.0.0.1:55052", FreeMiB: 5784, Remote: true},
	}
	remotes := []remoteGenerativeCandidate{
		{
			Node: types.GenerativeNodeCapability{
				NodeID:            "linux",
				NodeName:          "Worker-Linux",
				MemoryAvailableMB: 3600,
			},
			Relay:  types.GenerativeRelayStatus{LocalEndpoint: "127.0.0.1:55052"},
			Device: devices[1],
		},
	}
	plan, err := planAutomaticGenerativeRunN(12*1024*1024*1024, 512, false, types.GenerativeNodeCapability{}, devices, remotes)
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Selected) != 2 {
		t.Fatalf("expected 2 selected devices, got %d", len(plan.Selected))
	}
	remote := plan.Selected[1]
	if remote.RuntimeFreeMiB != 5784 {
		t.Fatalf("runtime free mismatch: got %d", remote.RuntimeFreeMiB)
	}
	if remote.OSAvailableMiB != 3600 {
		t.Fatalf("OS available mismatch: got %d", remote.OSAvailableMiB)
	}
	if remote.CapacityBasisMiB != 3600 {
		t.Fatalf("expected OS clamp basis 3600 MiB, got %d", remote.CapacityBasisMiB)
	}
	if remote.UsableMiB != 3088 {
		t.Fatalf("expected 3088 MiB usable after reserve, got %d", remote.UsableMiB)
	}
}

func TestZeroMemoryBLASIsNotCapacityDevice(t *testing.T) {
	devices := []llamaDevice{
		{ID: "MTL0", Name: "Apple M2 Pro", TotalMiB: 10922, FreeMiB: 10922},
		{ID: "BLAS", Name: "Accelerate", TotalMiB: 0, FreeMiB: 0},
	}
	if !hasZeroMemoryComputeBackend(devices) {
		t.Fatal("expected zero-memory Accelerate backend to be recognized as non-capacity compute backend")
	}
}

func TestResolveGenerativeCandidatesGraceRecoversTransientOffline(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/generative/fabric" {
			http.NotFound(w, r)
			return
		}
		state := "OFFLINE"
		if calls.Add(1) >= 2 {
			state = "ONLINE"
		}
		now := time.Now().UTC()
		summary := types.GenerativeFabricSummary{Nodes: []types.GenerativeNodeCapability{
			{NodeID: "mac", NodeName: "Mac", NodeState: "ONLINE", CoordinatorCapable: true, LastSeen: now, TelemetryUpdatedAt: now},
			{NodeID: "win", NodeName: "Worker-Windows", NodeState: state, WorkerCapable: true, LastSeen: now, TelemetryUpdatedAt: now},
		}}
		_ = json.NewEncoder(w).Encode(summary)
	}))
	defer srv.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	remote, _, err := resolveGenerativeCandidatesWithGrace(ctx, srv.URL, "Worker-Windows", "", 2*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if len(remote) != 1 || remote[0].NodeName != "Worker-Windows" {
		t.Fatalf("unexpected recovered candidates: %+v", remote)
	}
	if calls.Load() < 2 {
		t.Fatalf("expected retry, calls=%d", calls.Load())
	}
}

func TestPlanAutomaticGenerativeRunNClampsLocalToOSAvailable(t *testing.T) {
	devices := []llamaDevice{
		{ID: "MTL0", Name: "Apple M2 Pro", FreeMiB: 10922},
		{ID: "RPC0", Name: "127.0.0.1:55052", FreeMiB: 5784, Remote: true},
	}
	localNode := types.GenerativeNodeCapability{NodeName: "Primary", MemoryAvailableMB: 7400}
	remotes := []remoteGenerativeCandidate{{
		Node:   types.GenerativeNodeCapability{NodeName: "Worker", MemoryAvailableMB: 3600},
		Relay:  types.GenerativeRelayStatus{LocalEndpoint: "127.0.0.1:55052"},
		Device: devices[1],
	}}
	plan, err := planAutomaticGenerativeRunN(8*1024*1024*1024, 512, false, localNode, devices, remotes)
	if err != nil {
		t.Fatal(err)
	}
	local := plan.Selected[0]
	if local.OSAvailableMiB != 7400 {
		t.Fatalf("OS available mismatch: got %d", local.OSAvailableMiB)
	}
	if local.CapacityBasisMiB != 7400 {
		t.Fatalf("expected local OS clamp basis 7400 MiB, got %d", local.CapacityBasisMiB)
	}
	if local.UsableMiB != 6888 {
		t.Fatalf("expected local usable 6888 MiB, got %d", local.UsableMiB)
	}
}

func TestLocalNPlanFitsWithoutRemoteDevices(t *testing.T) {
	devices := []llamaDevice{{ID: "MTL0", Name: "Apple M2 Pro", FreeMiB: 10922}}
	local := types.GenerativeNodeCapability{NodeID: "mac", NodeName: "Primary-Mac", MemoryAvailableMB: 8722}
	plan, fits, err := planLocalGenerativeRunN(2382*1024*1024, 512, local, devices)
	if err != nil {
		t.Fatal(err)
	}
	if !fits || plan.Classification != "FITS-LOCAL" || plan.Distributed {
		t.Fatalf("unexpected local plan: %+v fits=%v", plan, fits)
	}
	if len(plan.Selected) != 1 || plan.Selected[0].UsableMiB != 8210 || plan.Selected[0].Share != 1 {
		t.Fatalf("unexpected local selected device: %+v", plan.Selected)
	}
}

func TestSelectRemoteNodesForCapacityActivatesOnlyWindowsFor14B(t *testing.T) {
	nodes := []types.GenerativeNodeCapability{
		{NodeID: "linux", NodeName: "Worker-Linux", MemoryAvailableMB: 4585},
		{NodeID: "win", NodeName: "Worker-Windows", MemoryAvailableMB: 8082},
	}
	selected, remaining := selectRemoteNodesForCapacity(nodes, 512, 8391, 11561, false)
	if len(selected) != 1 || selected[0].NodeName != "Worker-Windows" {
		t.Fatalf("expected only Worker-Windows, got selected=%+v remaining=%+v", selected, remaining)
	}
	if len(remaining) != 1 || remaining[0].NodeName != "Worker-Linux" {
		t.Fatalf("expected Linux to remain inactive, got %+v", remaining)
	}
}

func TestSelectRemoteNodesForCapacityActivatesBothFor30B(t *testing.T) {
	nodes := []types.GenerativeNodeCapability{
		{NodeID: "linux", NodeName: "Worker-Linux", MemoryAvailableMB: 4585},
		{NodeID: "win", NodeName: "Worker-Windows", MemoryAvailableMB: 8082},
	}
	selected, remaining := selectRemoteNodesForCapacity(nodes, 512, 8391, 17698, false)
	if len(selected) != 2 {
		t.Fatalf("expected both workers, got selected=%+v remaining=%+v", selected, remaining)
	}
	if selected[0].NodeName != "Worker-Windows" || selected[1].NodeName != "Worker-Linux" {
		t.Fatalf("unexpected activation order: %+v", selected)
	}
	if len(remaining) != 0 {
		t.Fatalf("expected no remaining workers, got %+v", remaining)
	}
}

func TestGenerativeNodeFreshnessIssue(t *testing.T) {
	now := time.Now().UTC()
	fresh := types.GenerativeNodeCapability{
		NodeName: "Worker-Windows", NodeState: "ONLINE",
		LastSeen: now.Add(-2 * time.Second), TelemetryUpdatedAt: now.Add(-3 * time.Second),
	}
	if issue := generativeNodeFreshnessIssue(fresh, now); issue != "" {
		t.Fatalf("fresh node unexpectedly rejected: %s", issue)
	}
	stale := fresh
	stale.LastSeen = now.Add(-20 * time.Second)
	if issue := generativeNodeFreshnessIssue(stale, now); !strings.Contains(issue, "heartbeat is stale") {
		t.Fatalf("expected stale heartbeat issue, got %q", issue)
	}
	stale = fresh
	stale.TelemetryUpdatedAt = now.Add(-20 * time.Second)
	if issue := generativeNodeFreshnessIssue(stale, now); !strings.Contains(issue, "telemetry is stale") {
		t.Fatalf("expected stale telemetry issue, got %q", issue)
	}
}

func TestCandidateSubsetDoesNotWaitForUnneededOfflineNode(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if r.URL.Path != "/v1/generative/fabric" {
			http.NotFound(w, r)
			return
		}
		now := time.Now().UTC()
		summary := types.GenerativeFabricSummary{Nodes: []types.GenerativeNodeCapability{
			{NodeID: "mac", NodeName: "Mac", NodeState: "ONLINE", CoordinatorCapable: true, LastSeen: now, TelemetryUpdatedAt: now},
			{NodeID: "linux", NodeName: "Worker-Linux", NodeState: "ONLINE", WorkerCapable: true, MemoryAvailableMB: 4600, LastSeen: now, TelemetryUpdatedAt: now},
			{NodeID: "win", NodeName: "Worker-Windows", NodeState: "OFFLINE", WorkerCapable: true, MemoryAvailableMB: 8000, LastSeen: now.Add(-time.Minute), TelemetryUpdatedAt: now.Add(-time.Minute)},
		}}
		_ = json.NewEncoder(w).Encode(summary)
	}))
	defer srv.Close()

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	ready, _, total, recovering, err := resolveGenerativeCandidatesForCapacityWithGrace(
		ctx, srv.URL, "", "Worker-Linux,Worker-Windows", 15*time.Second,
		512, 8000, 11561, false,
	)
	if err != nil {
		t.Fatal(err)
	}
	if total != 2 || recovering != 1 {
		t.Fatalf("expected 2 total / 1 recovering, got total=%d recovering=%d", total, recovering)
	}
	if len(ready) != 1 || ready[0].NodeName != "Worker-Linux" {
		t.Fatalf("expected fresh Linux subset, got %+v", ready)
	}
	if calls.Load() != 1 {
		t.Fatalf("expected no recovery wait when fresh capacity is sufficient, calls=%d", calls.Load())
	}
}

func TestCapacityReadinessWaitsOnlyWhenRecoveringCapacityIsNeeded(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/generative/fabric" {
			http.NotFound(w, r)
			return
		}
		call := calls.Add(1)
		now := time.Now().UTC()
		state := "OFFLINE"
		lastSeen := now.Add(-time.Minute)
		telemetry := now.Add(-time.Minute)
		if call >= 2 {
			state = "ONLINE"
			lastSeen = now
			telemetry = now
		}
		summary := types.GenerativeFabricSummary{Nodes: []types.GenerativeNodeCapability{
			{NodeID: "mac", NodeName: "Mac", NodeState: "ONLINE", CoordinatorCapable: true, LastSeen: now, TelemetryUpdatedAt: now},
			{NodeID: "win", NodeName: "Worker-Windows", NodeState: state, WorkerCapable: true, MemoryAvailableMB: 8000, LastSeen: lastSeen, TelemetryUpdatedAt: telemetry},
		}}
		_ = json.NewEncoder(w).Encode(summary)
	}))
	defer srv.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	ready, _, total, recovering, err := resolveGenerativeCandidatesForCapacityWithGrace(
		ctx, srv.URL, "Worker-Windows", "", 2*time.Second,
		512, 5000, 11561, false,
	)
	if err != nil {
		t.Fatal(err)
	}
	if len(ready) != 1 || ready[0].NodeName != "Worker-Windows" {
		t.Fatalf("unexpected recovered candidate set: %+v", ready)
	}
	if total != 1 || recovering != 0 {
		t.Fatalf("expected fully recovered 1/1 set, total=%d recovering=%d", total, recovering)
	}
	if calls.Load() < 2 {
		t.Fatalf("expected at least one retry, calls=%d", calls.Load())
	}
}

func TestAdaptiveMemoryReserveUsesTenPercentWithBounds(t *testing.T) {
	if got := adaptiveMemoryReserveMiB(16384, 8000); got != 1639 {
		t.Fatalf("expected 1639 MiB adaptive reserve for 16 GiB node, got %d", got)
	}
	if got := adaptiveMemoryReserveMiB(4096, 3000); got != 512 {
		t.Fatalf("expected 512 MiB floor, got %d", got)
	}
	if got := adaptiveMemoryReserveMiB(65536, 50000); got != 4096 {
		t.Fatalf("expected 4096 MiB cap, got %d", got)
	}
}

func TestMemoryReservePolicyParsesFixedAndPercentOverrides(t *testing.T) {
	policy, err := parseMemoryReservePolicy("Primary-Mac=2GiB,Worker-Windows=20%,Worker-Linux=768MiB", false, 512)
	if err != nil {
		t.Fatal(err)
	}
	mac := policy.reserveForNode(types.GenerativeNodeCapability{NodeName: "Primary-Mac", MemoryTotalMB: 16384}, 9000, true)
	if mac.MiB != 2048 {
		t.Fatalf("expected Mac reserve 2048 MiB, got %d", mac.MiB)
	}
	win := policy.reserveForNode(types.GenerativeNodeCapability{NodeName: "Worker-Windows", MemoryTotalMB: 16000}, 9000, false)
	if win.MiB != 3200 {
		t.Fatalf("expected Windows reserve 3200 MiB, got %d", win.MiB)
	}
	linux := policy.reserveForNode(types.GenerativeNodeCapability{NodeName: "Worker-Linux", MemoryTotalMB: 5600}, 4000, false)
	if linux.MiB != 768 {
		t.Fatalf("expected Linux reserve 768 MiB, got %d", linux.MiB)
	}
}

func TestAdaptivePlanUsesPerNodeReserve(t *testing.T) {
	devices := []llamaDevice{
		{ID: "MTL0", Name: "Apple M2 Pro", FreeMiB: 10922},
		{ID: "RPC0", Name: "127.0.0.1:55052", FreeMiB: 9000, Remote: true},
	}
	local := types.GenerativeNodeCapability{NodeID: "mac", NodeName: "Primary-Mac", MemoryTotalMB: 16384, MemoryAvailableMB: 9000}
	remoteNode := types.GenerativeNodeCapability{NodeID: "win", NodeName: "Worker-Windows", MemoryTotalMB: 16000, MemoryAvailableMB: 9000}
	remotes := []remoteGenerativeCandidate{{
		Node: remoteNode, Relay: types.GenerativeRelayStatus{LocalEndpoint: "127.0.0.1:55052"}, Device: devices[1],
	}}
	policy, err := parseMemoryReservePolicy("Worker-Windows=20%", false, 512)
	if err != nil {
		t.Fatal(err)
	}
	plan, err := planAutomaticGenerativeRunNWithPolicy(10*1024*1024*1024, policy, false, local, devices, remotes)
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Selected) != 2 {
		t.Fatalf("expected local + Windows, got %+v", plan.Selected)
	}
	if plan.Selected[0].ReserveMiB != 1639 {
		t.Fatalf("unexpected adaptive Primary reserve: %d", plan.Selected[0].ReserveMiB)
	}
	if plan.Selected[1].ReserveMiB != 3200 {
		t.Fatalf("unexpected Windows override reserve: %d", plan.Selected[1].ReserveMiB)
	}
}

func TestServeInstanceGuardRejectsSecondServeAndReleases(t *testing.T) {
	first, err := acquireServeInstanceGuard("127.0.0.1", 8081, "/tmp/model-a.gguf")
	if err != nil {
		t.Skipf("serve guard port unavailable in test environment: %v", err)
	}
	defer first.Close()
	if _, err := acquireServeInstanceGuard("127.0.0.1", 8082, "/tmp/model-b.gguf"); err == nil {
		t.Fatal("expected second SERVE instance to be rejected")
	} else if !strings.Contains(err.Error(), "already running") {
		t.Fatalf("unexpected second-SERVE error: %v", err)
	}
	first.Close()
	time.Sleep(20 * time.Millisecond)
	second, err := acquireServeInstanceGuard("127.0.0.1", 8082, "/tmp/model-b.gguf")
	if err != nil {
		t.Fatalf("expected guard to be reusable after release: %v", err)
	}
	second.Close()
}

func TestAutomaticRPCWorkerStartRequestsPersistentCache(t *testing.T) {
	// Automatic fabrics must request llama.cpp's worker-local persistent
	// tensor cache.
	w := types.WorkloadSpec{Type: executor.TaskLlamaRPCStart, RPCPort: 50052, RPCCache: true}
	if !w.RPCCache {
		t.Fatal("automatic RPC worker start must enable persistent tensor cache")
	}
}

func TestDefaultNibiaModelID(t *testing.T) {
	cases := map[string]string{
		"/tmp/Qwen3-14B-Q6_K.gguf":  "Qwen3-14B-Q6_K",
		"/tmp/qwen3-4b-nibia.gguf":  "qwen3-4b-nibia",
		"/tmp/My Model Q4_K_M.gguf": "My-Model-Q4_K_M",
	}
	for in, want := range cases {
		if got := defaultNibiaModelID(in); got != want {
			t.Fatalf("modelID(%q)=%q want %q", in, got, want)
		}
	}
}

func TestServeLoadTrackerDetectsModelLoaded(t *testing.T) {
	tr := newServeLoadTracker()
	tr.Observe([]byte("loading model...\n"))
	select {
	case <-tr.Done():
		t.Fatal("tracker must not be ready before model loaded")
	default:
	}
	tr.Observe([]byte("llama_server: model loaded\n"))
	select {
	case <-tr.Done():
	case <-time.After(100 * time.Millisecond):
		t.Fatal("tracker did not detect model loaded")
	}
}

func TestServeHostIsLoopback(t *testing.T) {
	for _, h := range []string{"127.0.0.1", "localhost", "::1"} {
		if !serveHostIsLoopback(h) {
			t.Fatalf("expected %q to be loopback", h)
		}
	}
	if serveHostIsLoopback("0.0.0.0") {
		t.Fatal("0.0.0.0 must not be treated as loopback")
	}
}

func TestLlamaServerArgsUseLocalCORS(t *testing.T) {
	plan := automaticGenerativePlanN{Selected: []plannedGenerativeDevice{{Device: llamaDevice{ID: "MTL0"}, Local: true}}}
	got := buildAutomaticLlamaServerArgsN("/tmp/model.gguf", "Model-ID", 4096, "none", "127.0.0.1", 8081, plan)
	joined := strings.Join(got, " ")
	if !strings.Contains(joined, "--cors-origins localhost") {
		t.Fatalf("expected localhost CORS, got %q", joined)
	}
}

func TestCleanModelDisplayName(t *testing.T) {
	if got := cleanModelDisplayName("/tmp/Qwen3-14B-Q6_K.gguf"); got != "Qwen3-14B-Q6_K" {
		t.Fatalf("unexpected model display name: %q", got)
	}
	if got := cleanModelDisplayName("/tmp/My Model Q4_K_M.gguf"); got != "My-Model-Q4_K_M" {
		t.Fatalf("unexpected whitespace-normalized display name: %q", got)
	}
}

func TestLlamaRuntimeShortLabel(t *testing.T) {
	if got := llamaRuntimeShortLabel("version: 0.4.0-dev (build 1, commit df03399b8)"); got != "df03399" {
		t.Fatalf("unexpected short runtime label: %q", got)
	}
}

func TestCPUOnlyLocalPlanUsesOSMemoryWhenLlamaListsNoDevices(t *testing.T) {
	local := types.GenerativeNodeCapability{
		NodeID:            "linux",
		NodeName:          "Linux-Primary",
		OS:                "linux",
		Arch:              "amd64",
		MemoryTotalMB:     5600,
		MemoryAvailableMB: 3800,
	}
	plan, fits, err := planLocalGenerativeRunN(2382*1024*1024, 512, local, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !fits || plan.Classification != "FITS-LOCAL" || plan.Distributed {
		t.Fatalf("unexpected CPU-only local plan: %+v fits=%v", plan, fits)
	}
	if len(plan.Selected) != 1 {
		t.Fatalf("expected exactly one local execution domain, got %+v", plan.Selected)
	}
	selected := plan.Selected[0]
	if selected.Device.ID != "CPU0" || !selected.Device.CPUOnly {
		t.Fatalf("expected NIBIA CPU0 pseudo-device, got %+v", selected.Device)
	}
	if selected.RuntimeFreeMiB != 0 {
		t.Fatalf("CPU-only runtime free must remain 0 (not reported by llama.cpp), got %d", selected.RuntimeFreeMiB)
	}
	if selected.CapacityBasisMiB != 3800 || selected.UsableMiB != 3288 {
		t.Fatalf("unexpected CPU memory accounting: %+v", selected)
	}
	if selected.Share != 1 {
		t.Fatalf("expected local share 1.0, got %.3f", selected.Share)
	}
}

func TestCPUOnlyServeArgsUseDeviceNone(t *testing.T) {
	plan := automaticGenerativePlanN{Selected: []plannedGenerativeDevice{{
		Device: llamaDevice{ID: "CPU0", Name: "System CPU / RAM", CPUOnly: true},
		Local:  true,
	}}}
	args := buildAutomaticLlamaServerArgsN("/tmp/model.gguf", "Model-ID", 4096, "none", "127.0.0.1", 8081, plan)
	joined := strings.Join(args, " ")
	for _, want := range []string{"-dev none", "-ngl 0", "-fit off"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("CPU-only server args missing %q: %q", want, joined)
		}
	}
	if strings.Contains(joined, "CPU0") {
		t.Fatalf("NIBIA pseudo-device CPU0 must never be passed to llama.cpp: %q", joined)
	}
}

func TestCPUOnlyCLIArgsUseDeviceNone(t *testing.T) {
	plan := automaticGenerativePlanN{Selected: []plannedGenerativeDevice{{
		Device: llamaDevice{ID: "CPU0", Name: "System CPU / RAM", CPUOnly: true},
		Local:  true,
	}}}
	args := buildAutomaticLlamaArgsN("/tmp/model.gguf", "hello", 32, 4096, "none", plan, false)
	joined := strings.Join(args, " ")
	for _, want := range []string{"-dev none", "-ngl 0", "-fit off"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("CPU-only CLI args missing %q: %q", want, joined)
		}
	}
	if strings.Contains(joined, "CPU0") {
		t.Fatalf("NIBIA pseudo-device CPU0 must never be passed to llama.cpp: %q", joined)
	}
}

func TestCPUOnlyAutomaticPlanExpandsThroughRPC(t *testing.T) {
	local := types.GenerativeNodeCapability{
		NodeID:            "linux",
		NodeName:          "Linux-Primary",
		MemoryTotalMB:     5600,
		MemoryAvailableMB: 3800,
	}
	remote := remoteGenerativeCandidate{
		Node:   types.GenerativeNodeCapability{NodeID: "win", NodeName: "Worker-Windows", MemoryTotalMB: 16000, MemoryAvailableMB: 7000},
		Relay:  types.GenerativeRelayStatus{LocalEndpoint: "127.0.0.1:55052"},
		Device: llamaDevice{ID: "RPC0", Name: "127.0.0.1:55052", FreeMiB: 6500, Remote: true},
	}
	plan, err := planAutomaticGenerativeRunN(6000*1024*1024, 512, false, local, []llamaDevice{remote.Device}, []remoteGenerativeCandidate{remote})
	if err != nil {
		t.Fatal(err)
	}
	if !plan.Distributed || plan.Classification != "CAPACITY-EXPANDING" || len(plan.Selected) != 2 {
		t.Fatalf("unexpected CPU-primary distributed plan: %+v", plan)
	}
	if plan.Selected[0].Device.ID != "CPU0" || !plan.Selected[0].Device.CPUOnly {
		t.Fatalf("expected CPU0 local planning domain, got %+v", plan.Selected[0])
	}
	if plan.Selected[0].Share <= 0 || plan.Selected[1].Share <= 0 {
		t.Fatalf("expected non-zero CPU/RPC shares: %+v", plan.Selected)
	}
}

func TestCPUOnlyDistributedServeArgsNeverPassCPU0(t *testing.T) {
	plan := automaticGenerativePlanN{Distributed: true, Selected: []plannedGenerativeDevice{
		{Device: llamaDevice{ID: "CPU0", CPUOnly: true}, Local: true, UsableMiB: 3186, ReserveMiB: 579},
		{NodeID: "mac", NodeName: "Mac-Worker", RelayEndpoint: "127.0.0.1:55052", UsableMiB: 10033, ReserveMiB: 1639},
	}}
	args := buildAutomaticLlamaServerArgsN("/tmp/model.gguf", "Qwen3-14B", 4096, "none", "127.0.0.1", 8081, plan)
	joined := strings.Join(args, " ")
	for _, want := range []string{"--rpc 127.0.0.1:55052", "-dev RPC0", "-ts 10033", "-ngl auto", "-fit on", "-fitt 1639"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("CPU-primary distributed server args missing %q: %q", want, joined)
		}
	}
	if strings.Contains(joined, "CPU0") {
		t.Fatalf("CPU0 must never be passed to llama.cpp: %q", joined)
	}
}

func TestCPUOnlyDistributedSharesKeepPrimaryWithinSafeBudget(t *testing.T) {
	local := types.GenerativeNodeCapability{NodeID: "linux", NodeName: "Linux-Primary", MemoryTotalMB: 5600, MemoryAvailableMB: 3765}
	remote := remoteGenerativeCandidate{
		Node:   types.GenerativeNodeCapability{NodeID: "mac", NodeName: "Mac-Worker", MemoryTotalMB: 16384, MemoryAvailableMB: 11672},
		Relay:  types.GenerativeRelayStatus{LocalEndpoint: "127.0.0.1:55052"},
		Device: llamaDevice{ID: "RPC0", Name: "127.0.0.1:55052", FreeMiB: 11672, Remote: true},
	}
	plan, err := planAutomaticGenerativeRunNWithPolicy(11561*1024*1024, memoryReservePolicy{}, false, local, []llamaDevice{remote.Device}, []remoteGenerativeCandidate{remote})
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Selected) != 2 {
		t.Fatalf("expected two selected nodes: %+v", plan.Selected)
	}
	if got := plan.Selected[0].Share * 100; got < 27.4 || got > 27.8 {
		t.Fatalf("expected Primary share near 27.6%%, got %.2f%%", got)
	}
	if got := plan.Selected[1].Share * 100; got < 72.2 || got > 72.6 {
		t.Fatalf("expected remote share near 72.4%%, got %.2f%%", got)
	}
}

func TestInitialPromptEchoFilterDropsRuntimeEchoWhenGeneratedOutputFollows(t *testing.T) {
	f := newInitialPromptEchoFilter("/no_think\nReply exactly: NIBIA RELEASE OK")
	if f == nil {
		t.Fatal("expected prompt echo filter")
	}
	if got := f.Filter([]byte("\nReply exactly: NIBIA RELEASE OK\n\n"), false); len(got) != 0 {
		t.Fatalf("expected echo to remain buffered until generated output is visible, got %q", got)
	}
	got := string(f.Filter([]byte("NIBIA RELEASE OK\n"), false))
	if got != "NIBIA RELEASE OK\n" {
		t.Fatalf("unexpected filtered output %q", got)
	}
}

func TestInitialPromptEchoFilterPreservesNonEchoOutput(t *testing.T) {
	f := newInitialPromptEchoFilter("Reply exactly: NIBIA RELEASE OK")
	got := string(f.Filter([]byte("NIBIA RELEASE OK\n"), false))
	if got != "NIBIA RELEASE OK\n" {
		t.Fatalf("legitimate generated output changed: %q", got)
	}
}

func TestInitialPromptEchoFilterPreservesAmbiguousSoleMatchingLineOnFlush(t *testing.T) {
	f := newInitialPromptEchoFilter("NIBIA RELEASE OK")
	if got := f.Filter([]byte("NIBIA RELEASE OK\n"), false); len(got) != 0 {
		t.Fatalf("expected ambiguous matching line to be buffered, got %q", got)
	}
	got := string(f.Filter(nil, true))
	if got != "NIBIA RELEASE OK\n" {
		t.Fatalf("expected sole matching line to be preserved on final flush, got %q", got)
	}
}

func TestCachedDistributedLoadProgressHasNoPercentOrETA(t *testing.T) {
	got := formatCachedDistributedLoadProgress(7, 300*1024*1024, 5.2*1024*1024, 59*time.Second)
	if strings.Contains(got, "%") || strings.Contains(got, "remaining") || strings.Contains(got, "calculating") {
		t.Fatalf("cached distributed progress must not present misleading percentage/ETA: %q", got)
	}
	for _, want := range []string{"Loading", "transfer", "0.29 GiB", "5.2 MiB/s", "00:59"} {
		if !strings.Contains(got, want) {
			t.Fatalf("progress %q missing %q", got, want)
		}
	}
}

func TestRelayTunnelLabelDistinguishesStoppedRelayFromAgentTunnelPool(t *testing.T) {
	if got := relayTunnelLabel(types.GenerativeRelayStatus{Running: true}); got != "Ready tunnels" {
		t.Fatalf("running relay label = %q, want Ready tunnels", got)
	}
	if got := relayTunnelLabel(types.GenerativeRelayStatus{Running: false}); got != "Agent reverse tunnels" {
		t.Fatalf("stopped relay label = %q, want Agent reverse tunnels", got)
	}
}

func TestHydrateGenerativeRelayIdentityPreservesStatusAndFillsNodeName(t *testing.T) {
	node := types.GenerativeNodeCapability{NodeID: "node-1", NodeName: "Segundo-Nodo"}
	status := types.GenerativeRelayStatus{Managed: true, Running: false, ReadyTunnelCount: 4}
	got := hydrateGenerativeRelayIdentity(status, node)
	if got.NodeID != node.NodeID || got.NodeName != node.NodeName {
		t.Fatalf("relay identity = %q/%q, want %q/%q", got.NodeID, got.NodeName, node.NodeID, node.NodeName)
	}
	if got.Running || got.ReadyTunnelCount != 4 {
		t.Fatalf("relay status fields changed unexpectedly: %+v", got)
	}
}
