package main

import (
	"bytes"
	"context"
	"flag"
	"fmt"
	"io"
	"math"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/nibia-ai/fabric/internal/executor"
	"github.com/nibia-ai/fabric/internal/procstats"
	"github.com/nibia-ai/fabric/internal/types"
)

type llamaDevice struct {
	ID       string
	Name     string
	TotalMiB uint64
	FreeMiB  uint64
	Remote   bool
	CPUOnly  bool // NIBIA pseudo-device backed by host CPU + system RAM; not a llama.cpp offload device.
}

type automaticGenerativePlan struct {
	Classification  string
	Distributed     bool
	ModelMiB        uint64
	ReserveMiB      uint64
	Local           llamaDevice
	Remote          llamaDevice
	LocalUsableMiB  uint64
	RemoteUsableMiB uint64
	LocalShare      float64
	RemoteShare     float64
}

var llamaDeviceLineRE = regexp.MustCompile(`(?m)^\s*([A-Za-z0-9]+):\s*(.*?)\s*\((\d+)\s+MiB,\s*(\d+)\s+MiB free\)\s*$`)

func generativeDevicesCmd(args []string) {
	fs := newCommandFlagSet("fabric devices", "nibia fabric devices [options]")
	controller := fs.String("controller", "http://127.0.0.1:8080", "Controller URL")
	node := fs.String("node", "", "single remote RPC worker node name or id")
	nodes := fs.String("nodes", "", "comma-separated remote RPC worker nodes; empty means auto-discover all eligible workers")
	timeout := fs.Duration("timeout", 20*time.Second, "device discovery timeout")
	autoStart := fs.Bool("auto-start", true, "start managed RPC workers and relays when needed")
	nodeGrace := fs.Duration("node-grace", 15*time.Second, "grace window for a recently transient OFFLINE worker to recover")
	_ = fs.Parse(args)

	remoteNodes, _, err := resolveGenerativeCandidatesWithGrace(context.Background(), *controller, *node, *nodes, *nodeGrace)
	if err != nil {
		fmt.Fprintf(os.Stderr, "fabric devices: %v\n", err)
		os.Exit(1)
	}
	relays, err := prepareRemoteFabric(*controller, remoteNodes, *autoStart, 55052)
	if err != nil {
		fmt.Fprintf(os.Stderr, "prepare N-node fabric: %v\n", err)
		os.Exit(1)
	}
	var fabricCleanupOnce sync.Once
	cleanupFabric := func() {
		fabricCleanupOnce.Do(func() {
			if !*autoStart {
				return
			}
			for _, warning := range cleanupRemoteFabric(*controller, remoteNodes) {
				fmt.Fprintf(os.Stderr, "fabric cleanup warning: %s\n", warning)
			}
		})
	}
	llama, err := resolveLlamaBinaryAuto(context.Background(), "llama-cli")
	if err != nil {
		fmt.Fprintln(os.Stderr, "llama-cli runtime unavailable")
		cleanupFabric()
		os.Exit(1)
	}
	ctx, cancel := context.WithTimeout(context.Background(), *timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, llama, "--rpc", relayEndpoints(relays), "--list-devices")
	var out bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &out
	err = cmd.Run()
	text := out.String()
	devices := parseLlamaDevices(text)
	cleanupFabric()

	fmt.Println("NIBIA N-node llama.cpp Device Discovery")
	fmt.Println()
	fmt.Printf("Remote worker nodes:       %d\n", len(remoteNodes))
	for i, n := range remoteNodes {
		fmt.Printf("  %-18s %s -> %s\n", n.NodeName, n.PreferredAddress, relays[i].LocalEndpoint)
	}
	fmt.Printf("llama-cli:                 %s\n", llama)
	fmt.Printf("All RPC devices visible:   %t\n", allRemoteDevicesVisible(devices, relays))
	fmt.Println()
	fmt.Print(text)
	if hasZeroMemoryComputeBackend(devices) {
		fmt.Println("\nNote: zero-memory BLAS/Accelerate entries are compute backends, not capacity devices; NIBIA excludes them from memory planning.")
	}
	if err != nil {
		fmt.Fprintf(os.Stderr, "\nllama.cpp N-node device discovery failed: %v\n", err)
		os.Exit(1)
	}
	if !allRemoteDevicesVisible(devices, relays) {
		fmt.Fprintln(os.Stderr, "\none or more RPC devices were not recognized")
		os.Exit(1)
	}
}

func generativeRunCmd(args []string) {
	fs := newCommandFlagSet("run", "nibia run --model <file.gguf> [options]")
	controller := fs.String("controller", "http://127.0.0.1:8080", "Controller URL")
	node := fs.String("node", "", "single remote RPC worker node name or id; equivalent to a single-entry --nodes")
	nodes := fs.String("nodes", "", "comma-separated remote RPC worker nodes; empty means auto-discover all eligible workers")
	model := fs.String("model", "", "local GGUF model path on the Primary Node")
	prompt := fs.String("prompt", "", "initial prompt; required for one-shot RUN, optional with --session")
	tokens := fs.Int("tokens", 64, "maximum tokens to generate per response")
	ctxSize := fs.Int("ctx", 4096, "context size")
	loadMode := fs.String("load-mode", "none", "llama.cpp load mode: none or auto")
	reserveMiB := fs.Uint64("reserve-mb", 0, "legacy global fixed memory reserve override in MiB; omitted means adaptive reservation")
	memoryReserve := fs.String("memory-reserve", "", "per-node memory reservation overrides, e.g. primary=2GiB,worker-a=20%,worker-b=768MiB")
	forceDistributed := fs.Bool("force-distributed", false, "use remote devices even when the model fits locally")
	autoStart := fs.Bool("auto-start", true, "start managed RPC workers and relays when needed")
	showProgress := fs.Bool("progress", true, "show distributed model-loading progress")
	verbose := fs.Bool("verbose", false, "show detailed NIBIA planning and process telemetry")
	verboseRuntime := fs.Bool("verbose-runtime", false, "show raw llama.cpp startup/runtime output while loading (debugging)")
	session := fs.Bool("session", false, "keep llama-cli interactive after the first response for additional prompts")
	timeout := fs.Duration("timeout", 20*time.Minute, "maximum runtime; 0 disables the timeout")
	planningTimeout := fs.Duration("planning-timeout", 20*time.Second, "maximum time for each N-node llama.cpp device-discovery attempt")
	nodeGrace := fs.Duration("node-grace", 15*time.Second, "grace window for a recently transient OFFLINE worker to recover")
	allowRuntimeMismatch := fs.Bool("allow-runtime-mismatch", false, "development escape hatch: permit incompatible llama.cpp source identities across selected candidates")
	_ = fs.Parse(args)

	reservePolicy, err := parseMemoryReservePolicy(*memoryReserve, flagWasSet(fs, "reserve-mb"), *reserveMiB)
	if err != nil {
		fmt.Fprintf(os.Stderr, "--memory-reserve: %v\n", err)
		os.Exit(2)
	}

	if strings.TrimSpace(*model) == "" {
		fmt.Fprintln(os.Stderr, "--model is required")
		os.Exit(2)
	}
	if !*session && strings.TrimSpace(*prompt) == "" {
		fmt.Fprintln(os.Stderr, "--prompt is required for one-shot RUN; use --session for interactive terminal inference")
		os.Exit(2)
	}
	if *tokens <= 0 || *tokens > 4096 {
		fmt.Fprintln(os.Stderr, "--tokens must be between 1 and 4096")
		os.Exit(2)
	}
	if *ctxSize < 256 || *ctxSize > 1048576 {
		fmt.Fprintln(os.Stderr, "--ctx must be between 256 and 1048576")
		os.Exit(2)
	}
	mode := strings.ToLower(strings.TrimSpace(*loadMode))
	if mode != "none" && mode != "auto" {
		fmt.Fprintln(os.Stderr, "--load-mode must be none or auto")
		os.Exit(2)
	}
	if *timeout < 0 || *planningTimeout <= 0 {
		fmt.Fprintln(os.Stderr, "--timeout must be >= 0 and --planning-timeout must be > 0")
		os.Exit(2)
	}

	interruptCtx, stopSignals := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stopSignals()
	effectiveTimeout := *timeout
	if *session && !flagWasSet(fs, "timeout") {
		effectiveTimeout = 0
	}
	lifecycle := "ONE-SHOT"
	phase4 := "Generating response"
	if *session {
		lifecycle = "SESSION"
		phase4 = "Interactive session"
	}

	absoluteModel, err := filepath.Abs(*model)
	if err != nil {
		fmt.Fprintf(os.Stderr, "resolve model path: %v\n", err)
		os.Exit(1)
	}
	info, err := os.Stat(absoluteModel)
	if err != nil {
		fmt.Fprintf(os.Stderr, "model path: %v\n", err)
		os.Exit(1)
	}
	if info.IsDir() || !strings.EqualFold(filepath.Ext(absoluteModel), ".gguf") {
		fmt.Fprintln(os.Stderr, "--model must reference an existing local .gguf file")
		os.Exit(2)
	}

	fmt.Println("NIBIA N-node Distributed Inference")
	fmt.Printf("Lifecycle: %s\n", lifecycle)
	fmt.Println()
	fmt.Println("[1/4] Preparing fabric")

	remoteNodes, localNode, err := resolveGenerativeCandidatesWithGrace(interruptCtx, *controller, *node, *nodes, *nodeGrace)
	if err != nil {
		fmt.Fprintf(os.Stderr, "run: %v\n", err)
		os.Exit(1)
	}
	fmt.Printf("      Candidate workers: %d\n", len(remoteNodes))
	for _, n := range remoteNodes {
		addr := n.PreferredAddress
		if addr == "" {
			addr = "address unknown"
		}
		fmt.Printf("        %-18s %s/%s  %s  RAM %.1f/%.1f GiB\n", n.NodeName, n.OS, n.Arch, addr, float64(n.MemoryAvailableMB)/1024, float64(n.MemoryTotalMB)/1024)
	}

	relays, err := prepareRemoteFabric(*controller, remoteNodes, *autoStart, 55052)
	if err != nil {
		fmt.Fprintf(os.Stderr, "prepare N-node fabric: %v\n", err)
		os.Exit(1)
	}
	var fabricCleanupOnce sync.Once
	var fabricCleanupWarnings []string
	cleanupFabric := func() []string {
		fabricCleanupOnce.Do(func() {
			if !*autoStart {
				return
			}
			fabricCleanupWarnings = cleanupRemoteFabric(*controller, remoteNodes)
			for _, warning := range fabricCleanupWarnings {
				fmt.Fprintf(os.Stderr, "fabric cleanup warning: %s\n", warning)
			}
		})
		return fabricCleanupWarnings
	}
	exitWithCleanup := func(code int) {
		cleanupFabric()
		os.Exit(code)
	}
	for i, r := range relays {
		if *verbose {
			fmt.Printf("      %-18s RPC READY %s (%d tunnels)\n", remoteNodes[i].NodeName, r.LocalEndpoint, r.ReadyTunnelCount)
		} else {
			fmt.Printf("      %-18s RPC READY\n", remoteNodes[i].NodeName)
		}
	}
	fmt.Println("      ✓ Fabric ready")
	fmt.Println("      RPC tensor cache: enabled (persistent, worker-local)")

	llama, err := resolveLlamaBinaryAuto(context.Background(), "llama-cli")
	if err != nil {
		fmt.Fprintln(os.Stderr, "llama-cli runtime unavailable")
		exitWithCleanup(1)
	}
	localRuntime := llamaVersionLine(llama)
	if !*allowRuntimeMismatch {
		if err := validateRuntimeParity(localRuntime, remoteNodes); err != nil {
			fmt.Fprintf(os.Stderr, "runtime compatibility check failed: %v\n", err)
			fmt.Fprintln(os.Stderr, "NIBIA requires compatible llama.cpp source identities (version + Git commit) for N-node RPC execution. Use --allow-runtime-mismatch only for controlled development tests.")
			exitWithCleanup(1)
		}
	}
	if localRuntime != "" {
		fmt.Printf("      Runtime parity:  %s (version + commit compatible)\n", localRuntime)
	}

	fmt.Println()
	fmt.Println("[2/4] Planning execution")
	rpcEndpoints := relayEndpoints(relays)
	deviceOut, deviceErr := runDeviceDiscoveryWithProgress(interruptCtx, llama, rpcEndpoints, *planningTimeout)
	if interruptCtx.Err() != nil {
		fmt.Fprintln(os.Stderr, "\nNIBIA planning cancelled; llama.cpp device discovery was terminated.")
		exitWithCleanup(130)
	}
	devices := parseLlamaDevices(deviceOut)
	if deviceErr != nil || !allRemoteDevicesVisible(devices, relays) {
		fmt.Println("      ⚠ N-node RPC discovery did not complete cleanly.")
		fmt.Println("      Recovering once: restarting managed RPC workers and retrying discovery.")
		if err := restartRemoteWorkers(*controller, remoteNodes); err != nil {
			fmt.Fprintf(os.Stderr, "RPC worker recovery failed: %v\n", err)
			exitWithCleanup(1)
		}
		if err := waitForContext(interruptCtx, 500*time.Millisecond); err != nil {
			exitWithCleanup(130)
		}
		deviceOut, deviceErr = runDeviceDiscoveryWithProgress(interruptCtx, llama, rpcEndpoints, *planningTimeout)
		devices = parseLlamaDevices(deviceOut)
	}
	if deviceErr != nil || !allRemoteDevicesVisible(devices, relays) {
		fmt.Fprintln(os.Stderr, "N-node llama.cpp RPC preflight failed: one or more remote devices are not visible")
		if strings.TrimSpace(deviceOut) != "" {
			fmt.Fprintln(os.Stderr, deviceOut)
		}
		exitWithCleanup(1)
	}

	remoteDevices := attachRemoteDevices(devices, remoteNodes, relays)
	plan, err := planAutomaticGenerativeRunNWithPolicy(info.Size(), reservePolicy, *forceDistributed, localNode, devices, remoteDevices)
	if err != nil {
		fmt.Fprintf(os.Stderr, "automatic N-node generative plan: %v\n", err)
		exitWithCleanup(1)
	}
	printAutomaticPlanN(localNode, absoluteModel, info.Size(), *ctxSize, *tokens, mode, lifecycle, plan, *verbose)
	if plan.Classification == "INSUFFICIENT-FABRIC-CAPACITY" {
		fmt.Fprintln(os.Stderr, "\nNIBIA refused launch: selected N-node fabric capacity is insufficient for this model")
		exitWithCleanup(1)
	}

	powerGuards, guardedNodes, powerWarnings := acquireExecutionPowerGuards(*controller, plan, remoteNodes)
	fmt.Printf("      Power guard:     active on %d/%d selected nodes\n", guardedNodes, planNodeCount(plan))
	for _, warning := range powerWarnings {
		fmt.Printf("      ⚠ %s\n", warning)
	}

	cmdArgs := buildAutomaticLlamaArgsN(absoluteModel, *prompt, *tokens, *ctxSize, mode, plan, *session)
	baselines := map[string]types.GenerativeRelayStatus{}
	for _, d := range plan.Selected {
		if d.Local {
			continue
		}
		if st, err := getGenerativeRelayStatus(*controller, d.NodeID); err == nil {
			baselines[d.NodeID] = st
		}
	}

	fmt.Println()
	fmt.Println("[3/4] Loading model")
	if plan.Distributed {
		target := plannedRemoteTargetBytes(info.Size(), plan)
		fmt.Printf("      Remote tensors planned: %.2f GiB\n", float64(target)/(1024*1024*1024))
		fmt.Println("      RPC tensor cache:       resolving worker-local reuse")
		if *verbose {
			for _, line := range remoteTargetSummary(info.Size(), plan) {
				fmt.Println(line)
			}
		}
	} else {
		fmt.Println("      Local model initialization")
	}

	runCtx := interruptCtx
	cancelRun := func() {}
	if effectiveTimeout > 0 {
		runCtx, cancelRun = context.WithTimeout(interruptCtx, effectiveTimeout)
	}
	defer cancelRun()

	cmd := exec.CommandContext(runCtx, llama, cmdArgs...)
	// ONE-SHOT has its prompt on argv and does not need an interactive TTY.
	// Leaving stdin attached made llama-cli enter terminal-oriented rendering
	// paths that can bypass captured stdout/stderr. SESSION intentionally keeps
	// stdin attached so the user can continue sending prompts.
	if *session {
		cmd.Stdin = os.Stdin
	}
	tracker := newLoadTracker()
	renderer := newProgressRenderer(os.Stderr)
	gatePrompt := ""
	if !*session {
		gatePrompt = *prompt
	}
	outputGate := newRuntimeOutputGate(tracker, renderer, *verboseRuntime, gatePrompt)
	cmd.Stdout = observingWriter{dst: os.Stdout, gate: outputGate}
	cmd.Stderr = observingWriter{dst: os.Stderr, gate: outputGate, stderr: true}
	processDone := make(chan struct{})
	loadResultCh := make(chan loadProgressResult, 1)
	if *showProgress {
		if plan.Distributed {
			go monitorDistributedLoadN(*controller, baselines, plannedRemoteTargetBytes(info.Size(), plan), tracker.done, processDone, renderer, outputGate, phase4, *session, loadResultCh)
		} else {
			go monitorLocalLoad(tracker.done, processDone, renderer, outputGate, phase4, *session, loadResultCh)
		}
	} else {
		go monitorLoadPhaseOnly(tracker.done, processDone, outputGate, phase4, *session, loadResultCh)
	}

	// Inference accounting is process-scoped. System-wide node health remains
	// available through heartbeats for scheduling, but is not reported as if it
	// were NIBIA/llama.cpp consumption.
	remoteTelemetryResetAt := map[string]time.Time{}
	if *verbose {
		remoteTelemetryResetAt = resetSelectedRemoteProcessTelemetry(*controller, plan)
	}
	executionStarted := time.Now()
	if *session {
		fmt.Println("      The model will remain loaded in this llama-cli process until /exit or interruption.")
	}
	if err = cmd.Start(); err != nil {
		close(processDone)
		outputGate.Open()
		for _, warning := range powerGuards.Release() {
			fmt.Fprintf(os.Stderr, "power guard cleanup warning: %s\n", warning)
		}
		fmt.Fprintf(os.Stderr, "\nstart llama.cpp: %v\n", err)
		exitWithCleanup(1)
	}
	var localProcessMonitor *localProcessPeakMonitor
	if *verbose {
		localProcessMonitor = startLocalProcessPeakMonitor(cmd.Process.Pid)
	}
	err = cmd.Wait()
	close(processDone)
	for _, warning := range powerGuards.Release() {
		fmt.Fprintf(os.Stderr, "power guard cleanup warning: %s\n", warning)
	}
	var localPeakCPU float64
	var localPeakRSS uint64
	var remoteProcessPeaks []inferenceProcessPeak
	var missingRemoteTelemetry []string
	if *verbose {
		localPeakCPU, localPeakRSS = localProcessMonitor.Stop()
		remoteProcessPeaks, missingRemoteTelemetry = collectRemoteProcessPeaks(*controller, plan, remoteTelemetryResetAt)
	}

	var loadResult loadProgressResult
	select {
	case loadResult = <-loadResultCh:
	case <-time.After(200 * time.Millisecond):
	}
	outputGate.Flush(os.Stdout)
	if err != nil {
		if interruptCtx.Err() != nil {
			fmt.Fprintln(os.Stderr, "\nNIBIA execution cancelled; llama.cpp was terminated and RPC connections were closed.")
			exitWithCleanup(130)
		}
		if runCtx.Err() == context.DeadlineExceeded {
			fmt.Fprintf(os.Stderr, "\nNIBIA execution timed out after %s; llama.cpp was terminated.\n", effectiveTimeout)
			exitWithCleanup(1)
		}
		fmt.Fprintf(os.Stderr, "\ngenerative execution failed: %v\n", err)
		exitWithCleanup(1)
	}

	cleanupWarnings := cleanupFabric()

	fmt.Println()
	if *session {
		fmt.Println("✓ NIBIA session ended")
	} else {
		fmt.Println("✓ NIBIA inference completed")
	}
	fmt.Printf("Lifecycle:        %s\n", lifecycle)
	fmt.Printf("Execution mode:   %s\n", map[bool]string{true: "DISTRIBUTED", false: "LOCAL"}[plan.Distributed])
	fmt.Printf("Selected nodes:   %d\n", planNodeCount(plan))
	fmt.Println("Exit status:      0")
	if len(cleanupWarnings) == 0 {
		fmt.Println("Runtime cleanup:  complete")
	} else {
		fmt.Println("Runtime cleanup:  completed with warnings")
	}
	if loadResult.Duration > 0 {
		fmt.Printf("Model load time:  %s\n", formatClock(loadResult.Duration))
	}
	if *session {
		fmt.Printf("Session duration: %s\n", formatClock(time.Since(executionStarted)))
	}
	if *verbose && plan.Distributed {
		fmt.Println("\nPer-node RPC contribution:")
		for _, line := range selectedRelayStatusDeltas(*controller, plan, baselines) {
			fmt.Println(line)
		}
	}
	if *verbose {
		fmt.Println("\nInference process utilization (process-scoped):")
		localName := localNode.NodeName
		if strings.TrimSpace(localName) == "" {
			localName = "Local coordinator"
		}
		localShare := 1.0
		if len(plan.Selected) > 0 {
			localShare = plan.Selected[0].Share
		}
		fmt.Printf("  %-18s role=%-11s share=%5.1f%%  peak CPU=%6.1f%%  peak RSS=%.2f GiB\n",
			localName, "coordinator", localShare*100, localPeakCPU, float64(localPeakRSS)/(1024*1024*1024))
		for _, p := range remoteProcessPeaks {
			fmt.Printf("  %-18s role=%-11s share=%5.1f%%  peak CPU=%6.1f%%  peak RSS=%.2f GiB\n",
				p.NodeName, "RPC worker", p.Share*100, p.PeakCPU, float64(p.PeakRSS)/(1024*1024*1024))
		}
		for _, name := range missingRemoteTelemetry {
			fmt.Printf("  %-18s role=%-11s telemetry unavailable (worker status did not refresh in time)\n", name, "RPC worker")
		}
		fmt.Println("  Note: process CPU may exceed 100%; 100% ~= one fully utilized logical CPU.")
		fmt.Println("Shared address space: false")
	}
}

func flagWasSet(fs *flag.FlagSet, name string) bool {
	set := false
	fs.Visit(func(f *flag.Flag) {
		if f.Name == name {
			set = true
		}
	})
	return set
}

func runLocalDeviceDiscoveryWithProgress(parent context.Context, llama string, timeout time.Duration) (string, error) {
	if timeout <= 0 {
		timeout = 15 * time.Second
	}
	ctx, cancel := context.WithTimeout(parent, timeout)
	defer cancel()

	type result struct {
		out []byte
		err error
	}
	resultCh := make(chan result, 1)
	started := time.Now()
	fmt.Print("      Discovering local llama.cpp devices... 00:00 elapsed")
	go func() {
		cmd := exec.CommandContext(ctx, llama, "--list-devices")
		out, err := cmd.CombinedOutput()
		resultCh <- result{out: out, err: err}
	}()

	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		select {
		case res := <-resultCh:
			d := time.Since(started)
			if ctx.Err() == context.DeadlineExceeded {
				fmt.Printf("\r\x1b[2K      Discovering local llama.cpp devices... TIMEOUT after %s\n", formatClock(d))
				return string(res.out), fmt.Errorf("local device discovery timed out after %s", timeout)
			}
			if parent.Err() != nil {
				fmt.Printf("\r\x1b[2K      Discovering local llama.cpp devices... CANCELLED after %s\n", formatClock(d))
				return string(res.out), parent.Err()
			}
			if res.err != nil {
				fmt.Printf("\r\x1b[2K      Discovering local llama.cpp devices... FAILED after %s\n", formatClock(d))
				return string(res.out), res.err
			}
			fmt.Printf("\r\x1b[2K      Discovering local llama.cpp devices... ✓ %s\n", formatClock(d))
			return string(res.out), nil
		case <-ticker.C:
			fmt.Printf("\r\x1b[2K      Discovering local llama.cpp devices... %s elapsed", formatClock(time.Since(started)))
		}
	}
}

func runDeviceDiscoveryWithProgress(parent context.Context, llama, relayEndpoint string, timeout time.Duration) (string, error) {
	if timeout <= 0 {
		timeout = 15 * time.Second
	}
	ctx, cancel := context.WithTimeout(parent, timeout)
	defer cancel()

	type result struct {
		out []byte
		err error
	}
	resultCh := make(chan result, 1)
	started := time.Now()
	fmt.Print("      Discovering local + remote llama.cpp devices... 00:00 elapsed")
	go func() {
		cmd := exec.CommandContext(ctx, llama, "--rpc", relayEndpoint, "--list-devices")
		out, err := cmd.CombinedOutput()
		resultCh <- result{out: out, err: err}
	}()

	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		select {
		case res := <-resultCh:
			d := time.Since(started)
			if ctx.Err() == context.DeadlineExceeded {
				fmt.Printf("\r\x1b[2K      Discovering local + remote llama.cpp devices... TIMEOUT after %s\n", formatClock(d))
				return string(res.out), fmt.Errorf("device discovery timed out after %s", timeout)
			}
			if parent.Err() != nil {
				fmt.Printf("\r\x1b[2K      Discovering local + remote llama.cpp devices... CANCELLED after %s\n", formatClock(d))
				return string(res.out), parent.Err()
			}
			if res.err != nil {
				fmt.Printf("\r\x1b[2K      Discovering local + remote llama.cpp devices... FAILED after %s\n", formatClock(d))
				return string(res.out), res.err
			}
			fmt.Printf("\r\x1b[2K      Discovering local + remote llama.cpp devices... ✓ %s\n", formatClock(d))
			return string(res.out), nil
		case <-ticker.C:
			fmt.Printf("\r\x1b[2K      Discovering local + remote llama.cpp devices... %s elapsed", formatClock(time.Since(started)))
		}
	}
}

func printDeviceDiscoveryFailure(found types.GenerativeNodeCapability, relay types.GenerativeRelayStatus, output string, err error) {
	fmt.Printf("      Remote node:         %s\n", found.NodeName)
	fmt.Printf("      Authenticated relay: %s\n", relay.LocalEndpoint)
	fmt.Println("      Remote RPC device:   NOT VISIBLE")
	if strings.TrimSpace(output) != "" {
		fmt.Println()
		fmt.Print(output)
		if !strings.HasSuffix(output, "\n") {
			fmt.Println()
		}
	}
	if err != nil {
		fmt.Fprintf(os.Stderr, "\nllama.cpp RPC device preflight failed: %v\n", err)
	} else {
		fmt.Fprintln(os.Stderr, "\nllama.cpp did not report a remote RPC device")
	}
	fmt.Fprintln(os.Stderr, "The relay transport may still be healthy; this failure means the RPC protocol preflight itself did not complete.")
}

func restartManagedRPCWorker(controller string, found types.GenerativeNodeCapability) (executor.LlamaRPCWorkerStatus, error) {
	return restartManagedRPCWorkerWithCache(controller, found, true)
}

func restartManagedRPCWorkerWithCache(controller string, found types.GenerativeNodeCapability, cache bool) (executor.LlamaRPCWorkerStatus, error) {
	if _, err := runRPCWorkerAction(controller, found, executor.TaskLlamaRPCStop); err != nil {
		return executor.LlamaRPCWorkerStatus{}, fmt.Errorf("stop RPC worker: %w", err)
	}
	status, err := runRPCWorkerActionWithCache(controller, found, executor.TaskLlamaRPCStart, cache)
	if err != nil {
		return executor.LlamaRPCWorkerStatus{}, fmt.Errorf("start RPC worker: %w", err)
	}
	if !status.Running {
		return status, fmt.Errorf("restarted RPC worker did not enter running state")
	}
	return status, nil
}

func ensureManagedRPCWorker(controller string, found types.GenerativeNodeCapability) (executor.LlamaRPCWorkerStatus, error) {
	return ensureManagedRPCWorkerWithCache(controller, found, true)
}

func ensureManagedRPCWorkerWithCache(controller string, found types.GenerativeNodeCapability, cache bool) (executor.LlamaRPCWorkerStatus, error) {
	status, err := runRPCWorkerAction(controller, found, executor.TaskLlamaRPCStatus)
	if err == nil && status.Running {
		if status.Cache == cache {
			return status, nil
		}
		// Automatic NIBIA fabrics require a deterministic cache policy. If a
		// worker is already running with the opposite setting, restart only that
		// on-demand worker; the persistent Agent is not affected.
		return restartManagedRPCWorkerWithCache(controller, found, cache)
	}
	status, err = runRPCWorkerActionWithCache(controller, found, executor.TaskLlamaRPCStart, cache)
	if err != nil {
		return executor.LlamaRPCWorkerStatus{}, err
	}
	if !status.Running {
		return status, fmt.Errorf("managed RPC worker did not enter running state")
	}
	return status, nil
}

func resetSelectedRemoteProcessTelemetry(controller string, plan automaticGenerativePlanN) map[string]time.Time {
	resetAt := map[string]time.Time{}
	for _, d := range plan.Selected {
		if d.Local || d.NodeID == "" {
			continue
		}
		n := types.GenerativeNodeCapability{NodeID: d.NodeID, NodeName: d.NodeName}
		status, err := runRPCWorkerAction(controller, n, executor.TaskLlamaRPCTelemetryReset)
		if err != nil {
			fmt.Fprintf(os.Stderr, "      warning: could not reset process telemetry on %s: %v\n", d.NodeName, err)
			continue
		}
		resetAt[d.NodeID] = status.TelemetryStartedAt
	}
	return resetAt
}

type localProcessPeakMonitor struct {
	tracker *procstats.Tracker
	stop    chan struct{}
	done    chan struct{}
	once    sync.Once
}

func startLocalProcessPeakMonitor(pid int) *localProcessPeakMonitor {
	m := &localProcessPeakMonitor{tracker: procstats.NewTracker(pid), stop: make(chan struct{}), done: make(chan struct{})}
	go func() {
		defer close(m.done)
		_, _ = m.tracker.Sample()
		tick := time.NewTicker(500 * time.Millisecond)
		defer tick.Stop()
		for {
			select {
			case <-m.stop:
				_, _ = m.tracker.Sample()
				return
			case <-tick.C:
				_, _ = m.tracker.Sample()
			}
		}
	}()
	return m
}

func (m *localProcessPeakMonitor) Stop() (peakCPU float64, peakRSS uint64) {
	if m == nil {
		return 0, 0
	}
	m.once.Do(func() { close(m.stop) })
	<-m.done
	_, peakCPU, peakRSS = m.tracker.Values()
	return peakCPU, peakRSS
}

type inferenceProcessPeak struct {
	NodeName   string
	Role       string
	Share      float64
	PeakCPU    float64
	PeakRSS    uint64
	CurrentRSS uint64
}

func collectRemoteProcessPeaks(controller string, plan automaticGenerativePlanN, resetAt map[string]time.Time) ([]inferenceProcessPeak, []string) {
	wanted := map[string]plannedGenerativeDevice{}
	for _, d := range plan.Selected {
		if !d.Local && d.NodeID != "" {
			wanted[d.NodeID] = d
		}
	}
	found := map[string]inferenceProcessPeak{}

	// Process telemetry is published with the normal Agent heartbeat so the
	// coordinator does not have to schedule a second control-plane job just to
	// retrieve peaks after a heavy data-plane transfer. Wait for one fresh
	// heartbeat window; then fall back to a typed worker-status job for any node
	// that has not reported yet.
	deadline := time.Now().Add(12 * time.Second)
	for len(found) < len(wanted) && time.Now().Before(deadline) {
		summary, err := getGenerativeFabricSummary(controller)
		if err == nil {
			for _, n := range summary.Nodes {
				d, ok := wanted[n.NodeID]
				if !ok {
					continue
				}
				if expected := resetAt[n.NodeID]; !expected.IsZero() {
					if n.RPCWorkerTelemetryStartedAt.IsZero() || n.RPCWorkerTelemetryStartedAt.Before(expected.Add(-time.Second)) {
						continue
					}
				}
				if !n.RPCWorkerRunning && n.RPCWorkerPeakCPUPercent == 0 && n.RPCWorkerPeakRSSBytes == 0 {
					continue
				}
				found[n.NodeID] = inferenceProcessPeak{
					NodeName: d.NodeName, Role: "RPC worker", Share: d.Share,
					PeakCPU: n.RPCWorkerPeakCPUPercent, PeakRSS: n.RPCWorkerPeakRSSBytes, CurrentRSS: n.RPCWorkerProcessRSSBytes,
				}
			}
		}
		if len(found) < len(wanted) {
			time.Sleep(500 * time.Millisecond)
		}
	}

	var missing []string
	for nodeID, d := range wanted {
		if _, ok := found[nodeID]; ok {
			continue
		}
		n := types.GenerativeNodeCapability{NodeID: nodeID, NodeName: d.NodeName}
		var status executor.LlamaRPCWorkerStatus
		var err error
		for attempt := 0; attempt < 2; attempt++ {
			status, err = runRPCWorkerAction(controller, n, executor.TaskLlamaRPCStatus)
			if err == nil {
				break
			}
			time.Sleep(500 * time.Millisecond)
		}
		if err != nil {
			missing = append(missing, d.NodeName)
			continue
		}
		found[nodeID] = inferenceProcessPeak{
			NodeName: d.NodeName, Role: "RPC worker", Share: d.Share,
			PeakCPU: status.PeakCPUPercent, PeakRSS: status.PeakRSSBytes, CurrentRSS: status.ProcessRSSBytes,
		}
	}

	out := make([]inferenceProcessPeak, 0, len(found))
	for _, d := range plan.Selected {
		if d.Local {
			continue
		}
		if p, ok := found[d.NodeID]; ok {
			out = append(out, p)
		}
	}
	return out, missing
}

func runRPCWorkerAction(controller string, found types.GenerativeNodeCapability, workload string) (executor.LlamaRPCWorkerStatus, error) {
	return runRPCWorkerActionWithCache(controller, found, workload, false)
}

func runRPCWorkerActionWithCache(controller string, found types.GenerativeNodeCapability, workload string, cache bool) (executor.LlamaRPCWorkerStatus, error) {
	action := "status"
	switch workload {
	case executor.TaskLlamaRPCStart:
		action = "start"
	case executor.TaskLlamaRPCStop:
		action = "stop"
	case executor.TaskLlamaRPCTelemetryReset:
		action = "telemetry-reset"
	}
	req := types.JobRequest{
		Name: "llamacpp-rpc-" + action + "-" + strings.ToLower(strings.ReplaceAll(found.NodeName, " ", "-")),
		Requirements: types.JobRequirements{
			RequiredNodeID: found.NodeID,
			Capabilities:   []string{"executor-v1", "task-llamacpp-rpc-worker", "runtime:llama.cpp", "llamacpp:rpc-worker"},
			AllowBusy:      true,
		},
		Workload:    types.WorkloadSpec{Type: workload, RPCPort: 50052, RPCCache: cache && workload == executor.TaskLlamaRPCStart},
		MaxAttempts: 1,
	}
	var job types.ScheduledJob
	if err := apiJSON(http.MethodPost, controller, "/v1/jobs", req, &job); err != nil {
		return executor.LlamaRPCWorkerStatus{}, err
	}
	waitFor := 30 * time.Second
	if workload == executor.TaskLlamaRPCStop {
		waitFor = 15 * time.Second
	}
	var err error
	if workload == executor.TaskLlamaRPCStop {
		job, err = waitScheduledCleanupJob(controller, job, waitFor, found.NodeID)
	} else {
		job, err = waitScheduledJob(controller, job, waitFor)
	}
	if err != nil && workload == executor.TaskLlamaRPCStop {
		cancelScheduledJobBestEffort(controller, job.ID)
	}
	if err != nil {
		return executor.LlamaRPCWorkerStatus{}, err
	}
	if !strings.EqualFold(job.State, "SUCCEEDED") || job.Result == nil || !job.Result.Success {
		return executor.LlamaRPCWorkerStatus{}, fmt.Errorf("RPC worker %s ended in %s", action, job.State)
	}
	status, ok := decodeLlamaRPCWorkerStatus(job)
	if !ok {
		return executor.LlamaRPCWorkerStatus{}, fmt.Errorf("RPC worker %s returned no typed status", action)
	}
	return status, nil
}

func ensureGenerativeRelay(controller string, found types.GenerativeNodeCapability, localPort int) (types.GenerativeRelayStatus, error) {
	status, err := getGenerativeRelayStatus(controller, found.NodeID)
	if err == nil && status.Running && status.ReadyTunnelCount > 0 {
		return status, nil
	}
	if !status.Running {
		req := types.GenerativeRelayRequest{NodeID: found.NodeID, NodeName: found.NodeName, LocalPort: localPort, RemotePort: 50052}
		if err := apiJSON(http.MethodPost, controller, "/v1/generative/relays", req, &status); err != nil {
			return types.GenerativeRelayStatus{}, err
		}
	}
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		status, err = getGenerativeRelayStatus(controller, found.NodeID)
		if err == nil && status.Running && status.ReadyTunnelCount > 0 {
			return status, nil
		}
		time.Sleep(200 * time.Millisecond)
	}
	return status, fmt.Errorf("relay has no authenticated ready tunnels")
}

func getGenerativeRelayStatus(controller, nodeID string) (types.GenerativeRelayStatus, error) {
	var status types.GenerativeRelayStatus
	err := apiJSON(http.MethodGet, controller, "/v1/generative/relays/"+nodeID, nil, &status)
	return status, err
}

func parseLlamaDevices(output string) []llamaDevice {
	matches := llamaDeviceLineRE.FindAllStringSubmatch(output, -1)
	out := make([]llamaDevice, 0, len(matches))
	for _, m := range matches {
		total, err1 := strconv.ParseUint(m[3], 10, 64)
		free, err2 := strconv.ParseUint(m[4], 10, 64)
		if err1 != nil || err2 != nil {
			continue
		}
		id := strings.TrimSpace(m[1])
		out = append(out, llamaDevice{
			ID: id, Name: strings.TrimSpace(m[2]), TotalMiB: total, FreeMiB: free,
			Remote: strings.HasPrefix(strings.ToUpper(id), "RPC"),
		})
	}
	return out
}

func planAutomaticGenerativeRun(modelBytes int64, reserveMiB uint64, forceDistributed bool, devices []llamaDevice) (automaticGenerativePlan, error) {
	var local, remote llamaDevice
	for _, device := range devices {
		if device.FreeMiB == 0 {
			continue
		}
		if device.Remote {
			if device.FreeMiB > remote.FreeMiB {
				remote = device
			}
			continue
		}
		if device.FreeMiB > local.FreeMiB {
			local = device
		}
	}
	if local.ID == "" {
		return automaticGenerativePlan{}, fmt.Errorf("no local llama.cpp device with usable memory was found")
	}
	if remote.ID == "" {
		return automaticGenerativePlan{}, fmt.Errorf("no remote RPC device with usable memory was found")
	}
	modelMiB := uint64(math.Ceil(float64(modelBytes) / (1024 * 1024)))
	localUsable := subtractReserve(local.FreeMiB, reserveMiB)
	remoteUsable := subtractReserve(remote.FreeMiB, reserveMiB)
	aggregateUsable := localUsable + remoteUsable

	plan := automaticGenerativePlan{
		ModelMiB: modelMiB, ReserveMiB: reserveMiB,
		Local: local, Remote: remote,
		LocalUsableMiB: localUsable, RemoteUsableMiB: remoteUsable,
	}

	switch {
	case modelMiB <= localUsable && !forceDistributed:
		plan.Classification = "FITS-LOCAL"
		plan.Distributed = false
		plan.LocalShare = 1
	case modelMiB <= aggregateUsable:
		if modelMiB > localUsable {
			plan.Classification = "CAPACITY-EXPANDING"
		} else {
			plan.Classification = "DISTRIBUTED-FORCED"
		}
		plan.Distributed = true
		totalFree := float64(local.FreeMiB + remote.FreeMiB)
		plan.LocalShare = float64(local.FreeMiB) / totalFree
		plan.RemoteShare = float64(remote.FreeMiB) / totalFree
	default:
		plan.Classification = "INSUFFICIENT-FABRIC-CAPACITY"
		plan.Distributed = true
		totalFree := float64(local.FreeMiB + remote.FreeMiB)
		if totalFree > 0 {
			plan.LocalShare = float64(local.FreeMiB) / totalFree
			plan.RemoteShare = float64(remote.FreeMiB) / totalFree
		}
	}
	return plan, nil
}

func subtractReserve(value, reserve uint64) uint64 {
	if value <= reserve {
		return 0
	}
	return value - reserve
}

func printAutomaticPlan(found types.GenerativeNodeCapability, relay types.GenerativeRelayStatus, model string, modelBytes int64, ctx, tokens int, loadMode, lifecycle string, plan automaticGenerativePlan) {
	fmt.Printf("      Model:          %s\n", filepath.Base(model))
	fmt.Printf("      Model size:     %.2f GiB (%d MiB)\n", float64(modelBytes)/(1024*1024*1024), plan.ModelMiB)
	fmt.Printf("      Classification: %s\n", plan.Classification)
	fmt.Printf("      Execution:      %s\n", map[bool]string{true: "DISTRIBUTED", false: "LOCAL"}[plan.Distributed])
	fmt.Printf("      Lifecycle:      %s\n", lifecycle)
	fmt.Printf("      Local device:   %s %s (%d MiB free, %d MiB usable)\n", plan.Local.ID, plan.Local.Name, plan.Local.FreeMiB, plan.LocalUsableMiB)
	if plan.Distributed {
		fmt.Printf("      Remote node:    %s\n", found.NodeName)
		fmt.Printf("      Remote device:  %s %s (%d MiB free, %d MiB usable)\n", plan.Remote.ID, plan.Remote.Name, plan.Remote.FreeMiB, plan.RemoteUsableMiB)
		fmt.Printf("      Tensor split:   %.1f%% %s | %.1f%% %s\n", plan.LocalShare*100, plan.Local.ID, plan.RemoteShare*100, plan.Remote.ID)
		fmt.Printf("      Secure relay:   %s\n", relay.LocalEndpoint)
	}
	fmt.Printf("      Reserve/device: %d MiB\n", plan.ReserveMiB)
	fmt.Printf("      Planned memory: %.2f GiB aggregate node-local capacity\n", float64(plan.LocalUsableMiB+plan.RemoteUsableMiB)/1024)
	fmt.Printf("      Context/tokens: %d / %d\n", ctx, tokens)
	fmt.Printf("      Load mode:      %s\n", loadMode)
	fmt.Println("      Shared RAM:     no (independent node-local memory domains)")
	fmt.Println("      ✓ Plan ready")
}

func buildAutomaticLlamaArgs(model, prompt string, tokens, ctxSize int, loadMode, rpcEndpoint string, plan automaticGenerativePlan, session bool) []string {
	args := []string{
		"--simple-io",
		"-m", model,
	}
	if strings.TrimSpace(prompt) != "" {
		args = append(args, "-p", prompt)
	}
	args = append(args,
		"-n", strconv.Itoa(tokens),
		"-c", strconv.Itoa(ctxSize),
		"-dev", plan.Local.ID,
		"-ngl", "all",
		"-fit", "off",
		"--load-mode", loadMode,
	)
	if plan.Distributed {
		args = []string{
			"--simple-io",
			"--rpc", rpcEndpoint,
			"-m", model,
		}
		if strings.TrimSpace(prompt) != "" {
			args = append(args, "-p", prompt)
		}
		args = append(args,
			"-n", strconv.Itoa(tokens),
			"-c", strconv.Itoa(ctxSize),
			"-dev", plan.Local.ID+","+plan.Remote.ID,
			"-sm", "layer",
			"-ts", fmt.Sprintf("%d,%d", plan.Local.FreeMiB, plan.Remote.FreeMiB),
			"-ngl", "all",
			"-fit", "off",
			"--load-mode", loadMode,
		)
	}
	if !session {
		args = append(args, "-st")
	}
	return args
}

type loadTracker struct {
	mu   sync.Mutex
	tail string
	once sync.Once
	done chan struct{}
}

var ansiEscapeRE = regexp.MustCompile(`\x1b\[[0-9;?]*[ -/]*[@-~]`)
var interactivePromptRE = regexp.MustCompile(`(?m)^\s*\**>\s+`)

func newLoadTracker() *loadTracker {
	return &loadTracker{done: make(chan struct{})}
}

func (t *loadTracker) observe(p []byte) {
	t.mu.Lock()
	text := t.tail + string(p)
	if len(text) > 8192 {
		text = text[len(text)-8192:]
	}
	t.tail = text
	normalized := ansiEscapeRE.ReplaceAllString(text, "")
	normalized = strings.ReplaceAll(normalized, "\r", "\n")
	// Treat the interactive prompt (or the first generation marker) as the
	// runtime-ready boundary. Startup banner fields such as build/modalities
	// can arrive while the final RPC/model initialization is still in flight
	// and can otherwise keep repainting a load bar over generated text.
	loaded := interactivePromptRE.MatchString(normalized) ||
		strings.Contains(normalized, "[Start thinking]")
	t.mu.Unlock()
	if loaded {
		t.once.Do(func() { close(t.done) })
	}
}

func (t *loadTracker) Ready() bool {
	if t == nil {
		return false
	}
	select {
	case <-t.done:
		return true
	default:
		return false
	}
}

type progressRenderer struct {
	mu      sync.Mutex
	out     io.Writer
	active  bool
	lastLen int
}

func newProgressRenderer(out io.Writer) *progressRenderer {
	return &progressRenderer{out: out}
}

func (r *progressRenderer) clearLocked() {
	if !r.active || r.out == nil {
		return
	}
	fmt.Fprint(r.out, "\r\x1b[2K")
	r.active = false
	r.lastLen = 0
}

func (r *progressRenderer) Update(line string) {
	if r == nil || r.out == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.clearLocked()
	fmt.Fprint(r.out, line)
	r.active = true
	r.lastLen = len([]rune(line))
}

func (r *progressRenderer) Done(line string) {
	if r == nil || r.out == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.clearLocked()
	fmt.Fprintln(r.out, line)
}

func (r *progressRenderer) WriteRaw(dst io.Writer, p []byte) (int, error) {
	if r == nil {
		return dst.Write(p)
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.clearLocked()
	return dst.Write(p)
}

type pendingRuntimeChunk struct {
	dst    io.Writer
	data   []byte
	stderr bool
}

type initialPromptEchoFilter struct {
	candidates map[string]struct{}
	buffer     []byte
	decided    bool
}

func newInitialPromptEchoFilter(prompt string) *initialPromptEchoFilter {
	f := &initialPromptEchoFilter{candidates: map[string]struct{}{}}
	normalized := strings.ReplaceAll(prompt, "\r\n", "\n")
	for _, line := range strings.Split(normalized, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "/") {
			continue
		}
		f.candidates[line] = struct{}{}
	}
	if len(f.candidates) == 0 {
		return nil
	}
	return f
}

// Filter suppresses only an initial echoed input line, and only after another
// non-empty stdout line proves that generated output follows it. If the process
// ends with just one matching line, final=true preserves it because that could
// be a legitimate one-line model response rather than runtime echo.
func (f *initialPromptEchoFilter) Filter(p []byte, final bool) []byte {
	if f == nil || f.decided {
		return p
	}
	f.buffer = append(f.buffer, p...)
	text := strings.ReplaceAll(string(f.buffer), "\r\n", "\n")
	lines := strings.Split(text, "\n")
	limit := len(lines)
	if !final && !strings.HasSuffix(text, "\n") {
		limit--
	}
	first := -1
	for i := 0; i < limit; i++ {
		if strings.TrimSpace(lines[i]) != "" {
			first = i
			break
		}
	}
	if first < 0 {
		if final {
			f.decided = true
			out := append([]byte(nil), f.buffer...)
			f.buffer = nil
			return out
		}
		return nil
	}
	if _, match := f.candidates[strings.TrimSpace(lines[first])]; !match {
		f.decided = true
		out := append([]byte(nil), f.buffer...)
		f.buffer = nil
		return out
	}
	second := -1
	for i := first + 1; i < limit; i++ {
		if strings.TrimSpace(lines[i]) != "" {
			second = i
			break
		}
	}
	if second < 0 {
		if final {
			// Ambiguous: preserve a sole matching line rather than risk deleting a
			// legitimate exact-response answer.
			f.decided = true
			out := append([]byte(nil), f.buffer...)
			f.buffer = nil
			return out
		}
		return nil
	}
	f.decided = true
	out := []byte(strings.Join(lines[second:], "\n"))
	f.buffer = nil
	return out
}

type runtimeOutputGate struct {
	mu         sync.Mutex
	tracker    *loadTracker
	renderer   *progressRenderer
	verbose    bool
	open       bool
	pending    []pendingRuntimeChunk
	promptEcho *initialPromptEchoFilter
}

func newRuntimeOutputGate(tracker *loadTracker, renderer *progressRenderer, verbose bool, oneShotPrompt ...string) *runtimeOutputGate {
	g := &runtimeOutputGate{tracker: tracker, renderer: renderer, verbose: verbose}
	if len(oneShotPrompt) > 0 && strings.TrimSpace(oneShotPrompt[0]) != "" {
		g.promptEcho = newInitialPromptEchoFilter(oneShotPrompt[0])
	}
	return g
}

func (g *runtimeOutputGate) Write(dst io.Writer, p []byte, stderr bool) (int, error) {
	if g == nil {
		return dst.Write(p)
	}
	wasReady := g.tracker != nil && g.tracker.Ready()
	// Readiness must be driven by stdout only. llama.cpp writes startup/banner
	// text and, on some builds/TTY combinations, echoed prompt fragments to
	// stderr. stdout/stderr are delivered independently, so allowing stderr
	// to trip the ready boundary can open the gate before delayed stdout
	// startup output arrives. Otherwise the llama.cpp banner can appear late
	// and the load bar can keep repainting during generation.
	if g.tracker != nil && !stderr {
		g.tracker.observe(p)
	}
	nowReady := g.tracker != nil && g.tracker.Ready()

	g.mu.Lock()
	defer g.mu.Unlock()
	if g.verbose {
		if g.renderer != nil {
			_, err := g.renderer.WriteRaw(dst, p)
			return len(p), err
		}
		_, err := dst.Write(p)
		return len(p), err
	}
	if g.open {
		// Default UX owns the screen. llama.cpp writes much of its startup
		// banner and diagnostics to stderr, and stdout/stderr ordering is not
		// guaranteed. Suppress delayed startup noise after phase 4 while still
		// surfacing concise timing/error lines that are useful to the user.
		if stderr && !runtimeUserFacingStderr(p) {
			return len(p), nil
		}
		out := p
		if !stderr && g.promptEcho != nil {
			out = g.promptEcho.Filter(p, false)
			if len(out) == 0 {
				return len(p), nil
			}
		}
		if g.renderer != nil {
			_, err := g.renderer.WriteRaw(dst, out)
			return len(p), err
		}
		_, err := dst.Write(out)
		return len(p), err
	}

	// Suppress llama.cpp startup/banner text by default. If this chunk crosses
	// the runtime-ready boundary, preserve only any payload that appears after
	// the echoed prompt (or from the generation marker onward). This avoids
	// losing the first generated tokens when llama.cpp flushes prompt + output
	// in one write. Subsequent generated text is held briefly until phase 4.
	if nowReady {
		payload := p
		if !wasReady {
			payload = runtimePayloadAfterReadyBoundary(p)
		}
		if len(payload) > 0 {
			copyP := append([]byte(nil), payload...)
			g.pending = append(g.pending, pendingRuntimeChunk{dst: dst, data: copyP, stderr: stderr})
		}
	}
	return len(p), nil
}

func runtimePayloadAfterReadyBoundary(p []byte) []byte {
	if i := bytes.Index(p, []byte("[Start thinking]")); i >= 0 {
		return p[i:]
	}
	for _, marker := range [][]byte{[]byte("\n**> "), []byte("\n> ")} {
		if i := bytes.LastIndex(p, marker); i >= 0 {
			rest := p[i+len(marker):]
			if eol := bytes.IndexByte(rest, '\n'); eol >= 0 && eol+1 < len(rest) {
				return rest[eol+1:]
			}
		}
	}
	return nil
}

func (g *runtimeOutputGate) Open() {
	if g == nil {
		return
	}
	g.mu.Lock()
	if g.open {
		g.mu.Unlock()
		return
	}
	g.open = true
	pending := g.pending
	g.pending = nil

	// Advance the one-shot prompt filter over all buffered stdout while the
	// gate mutex is still held. This preserves chunk order if generation starts
	// concurrently with the transition from loading to phase 4.
	ready := make([]pendingRuntimeChunk, 0, len(pending))
	for _, chunk := range pending {
		if chunk.stderr && !runtimeUserFacingStderr(chunk.data) {
			continue
		}
		out := chunk.data
		if !chunk.stderr && g.promptEcho != nil {
			out = g.promptEcho.Filter(chunk.data, false)
			if len(out) == 0 {
				continue
			}
		}
		ready = append(ready, pendingRuntimeChunk{
			dst:    chunk.dst,
			data:   append([]byte(nil), out...),
			stderr: chunk.stderr,
		})
	}
	g.mu.Unlock()

	for _, chunk := range ready {
		if g.renderer != nil {
			_, _ = g.renderer.WriteRaw(chunk.dst, chunk.data)
		} else {
			_, _ = chunk.dst.Write(chunk.data)
		}
	}
}

func (g *runtimeOutputGate) Flush(dst io.Writer) {
	if g == nil || g.verbose || g.promptEcho == nil {
		return
	}
	g.mu.Lock()
	out := g.promptEcho.Filter(nil, true)
	g.mu.Unlock()
	if len(out) == 0 {
		return
	}
	if g.renderer != nil {
		_, _ = g.renderer.WriteRaw(dst, out)
	} else {
		_, _ = dst.Write(out)
	}
}

func runtimeUserFacingStderr(p []byte) bool {
	s := strings.ToLower(string(p))
	return strings.Contains(s, "[ prompt:") ||
		strings.Contains(s, "generation:") ||
		strings.Contains(s, "error:") ||
		strings.Contains(s, "failed") ||
		strings.Contains(s, "out of memory")
}

type observingWriter struct {
	dst    io.Writer
	gate   *runtimeOutputGate
	stderr bool
}

func (w observingWriter) Write(p []byte) (int, error) {
	if w.gate != nil {
		return w.gate.Write(w.dst, p, w.stderr)
	}
	return w.dst.Write(p)
}

type loadProgressResult struct {
	Duration       time.Duration
	Transferred    uint64
	TargetBytes    uint64
	AverageBps     float64
	EstimatedReady bool
}

func monitorDistributedLoad(controller, nodeID string, baselineBytes, targetRemoteBytes uint64, loadDone, processDone <-chan struct{}, renderer *progressRenderer, phase4 string, session bool, result chan<- loadProgressResult) {
	if targetRemoteBytes == 0 {
		result <- loadProgressResult{}
		return
	}
	start := time.Now()
	lastAt := start
	lastBytes := baselineBytes
	var emaBytesPerSecond float64
	var finalizingTicks int
	ticker := time.NewTicker(750 * time.Millisecond)
	defer ticker.Stop()

	finish := func(estimated bool) {
		status, err := getGenerativeRelayStatus(controller, nodeID)
		transferred := uint64(0)
		if err == nil && status.BytesToWorker >= baselineBytes {
			transferred = status.BytesToWorker - baselineBytes
		}
		duration := time.Since(start)
		avg := float64(transferred) / math.Max(duration.Seconds(), 0.001)
		label := "100.0%"
		suffix := ""
		if estimated {
			label = "~100%"
			suffix = " (transfer complete estimate)"
		}
		renderer.Done(fmt.Sprintf("      Remote shard [%s] %6s  %.2f GiB  avg %s  elapsed %s%s",
			renderProgressBar(100, 24), label,
			float64(transferred)/(1024*1024*1024), formatBytesRate(avg), formatClock(duration), suffix))
		fmt.Fprintf(os.Stderr, "      ✓ Model ready in %s\n\n[4/4] %s\n", formatClock(duration), phase4)
		if session {
			fmt.Fprintln(os.Stderr, "      Send additional prompts normally. Use /exit for a graceful NIBIA session close.")
		}
		result <- loadProgressResult{Duration: duration, Transferred: transferred, TargetBytes: targetRemoteBytes, AverageBps: avg, EstimatedReady: estimated}
	}

	for {
		select {
		case <-loadDone:
			finish(false)
			return
		case <-processDone:
			duration := time.Since(start)
			result <- loadProgressResult{Duration: duration, TargetBytes: targetRemoteBytes}
			return
		case now := <-ticker.C:
			status, err := getGenerativeRelayStatus(controller, nodeID)
			if err != nil || status.BytesToWorker < baselineBytes {
				continue
			}
			transferred := status.BytesToWorker - baselineBytes
			deltaBytes := status.BytesToWorker - lastBytes
			deltaSeconds := now.Sub(lastAt).Seconds()
			instant := float64(deltaBytes) / math.Max(deltaSeconds, 0.001)
			if emaBytesPerSecond == 0 {
				emaBytesPerSecond = instant
			} else {
				emaBytesPerSecond = 0.20*instant + 0.80*emaBytesPerSecond
			}
			pctRaw := float64(transferred) / float64(targetRemoteBytes) * 100
			pct := math.Min(99.0, pctRaw)
			remaining := "calculating..."
			if pctRaw >= 95 {
				remaining = "finalizing runtime"
			} else if emaBytesPerSecond > 1024*1024 {
				remainingDuration := time.Duration(float64(targetRemoteBytes-transferred)/emaBytesPerSecond) * time.Second
				remaining = "~" + formatClock(remainingDuration) + " remaining"
			}
			line := fmt.Sprintf("      Remote shard [%s] %5.1f%%  %.2f/%.2f GiB  %s  elapsed %s  %s",
				renderProgressBar(pct, 24), pct,
				float64(transferred)/(1024*1024*1024),
				float64(targetRemoteBytes)/(1024*1024*1024),
				formatBytesRate(emaBytesPerSecond), formatClock(now.Sub(start)), remaining)
			renderer.Update(line)

			// Layer granularity and RPC protocol overhead make the target approximate.
			// Once we are within 2% of the expected shard and transfer traffic has
			// collapsed for several samples, stop presenting a misleading ETA and
			// move to generation even if llama.cpp did not expose a clean load event.
			if pctRaw >= 98 && instant < 1024*1024 {
				finalizingTicks++
			} else {
				finalizingTicks = 0
			}
			if finalizingTicks >= 3 {
				finish(true)
				return
			}
			lastAt = now
			lastBytes = status.BytesToWorker
		}
	}
}

func monitorLocalLoad(loadDone, processDone <-chan struct{}, renderer *progressRenderer, gate *runtimeOutputGate, phase4 string, session bool, result chan<- loadProgressResult) {
	start := time.Now()
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	step := 0
	for {
		select {
		case <-loadDone:
			duration := time.Since(start)
			renderer.Done(fmt.Sprintf("      Local model     [%s] 100.0%%  elapsed %s", renderProgressBar(100, 24), formatClock(duration)))
			fmt.Fprintf(os.Stderr, "      ✓ Model ready in %s\n\n[4/4] %s\n", formatClock(duration), phase4)
			if session {
				fmt.Fprintln(os.Stderr, "      Send additional prompts normally. Use /exit for a graceful NIBIA session close.")
			}
			if gate != nil {
				gate.Open()
			}
			result <- loadProgressResult{Duration: duration}
			return
		case <-processDone:
			if gate != nil {
				gate.Open()
			}
			result <- loadProgressResult{Duration: time.Since(start)}
			return
		case <-ticker.C:
			step = (step + 1) % 24
			bar := strings.Repeat("░", 24)
			runes := []rune(bar)
			runes[step] = '█'
			renderer.Update(fmt.Sprintf("      Local model     [%s] loading  elapsed %s", string(runes), formatClock(time.Since(start))))
		}
	}
}

func monitorLoadPhaseOnly(loadDone, processDone <-chan struct{}, gate *runtimeOutputGate, phase4 string, session bool, result chan<- loadProgressResult) {
	start := time.Now()
	select {
	case <-loadDone:
		duration := time.Since(start)
		fmt.Fprintf(os.Stderr, "      ✓ Model ready in %s\n\n[4/4] %s\n", formatClock(duration), phase4)
		if session {
			fmt.Fprintln(os.Stderr, "      Send additional prompts normally. Use /exit for a graceful NIBIA session close.")
		}
		if gate != nil {
			gate.Open()
		}
		result <- loadProgressResult{Duration: duration}
	case <-processDone:
		if gate != nil {
			gate.Open()
		}
		result <- loadProgressResult{Duration: time.Since(start)}
	}
}

func renderProgressBar(percent float64, width int) string {
	if width < 1 {
		width = 1
	}
	if percent < 0 {
		percent = 0
	}
	if percent > 100 {
		percent = 100
	}
	filled := int(math.Round(percent / 100 * float64(width)))
	if filled > width {
		filled = width
	}
	return strings.Repeat("█", filled) + strings.Repeat("░", width-filled)
}

func formatBytesRate(bytesPerSecond float64) string {
	if bytesPerSecond <= 0 {
		return "0.0 MiB/s"
	}
	return fmt.Sprintf("%.1f MiB/s", bytesPerSecond/(1024*1024))
}

func formatClock(d time.Duration) string {
	if d < 0 {
		d = 0
	}
	seconds := int(d.Round(time.Second).Seconds())
	minutes := seconds / 60
	seconds %= 60
	if minutes >= 60 {
		hours := minutes / 60
		minutes %= 60
		return fmt.Sprintf("%02d:%02d:%02d", hours, minutes, seconds)
	}
	return fmt.Sprintf("%02d:%02d", minutes, seconds)
}

func requireUsableGenerativeRelay(controller, node string) (types.GenerativeNodeCapability, types.GenerativeRelayStatus) {
	found, err := resolveGenerativeNode(controller, node)
	if err != nil {
		fmt.Fprintf(os.Stderr, "run: %v\n", err)
		os.Exit(1)
	}
	if !found.WorkerCapable {
		fmt.Fprintf(os.Stderr, "node %s is not RPC-worker capable\n", found.NodeName)
		os.Exit(2)
	}

	var relay types.GenerativeRelayStatus
	path := "/v1/generative/relays/" + found.NodeID
	if err := apiJSON(http.MethodGet, controller, path, nil, &relay); err != nil {
		fmt.Fprintf(os.Stderr, "relay status failed: %v\n", err)
		os.Exit(1)
	}
	if !relay.Running || strings.TrimSpace(relay.LocalEndpoint) == "" {
		fmt.Fprintf(os.Stderr, "relay to %s is not running; start it with `nibia fabric relay start --node %s`\n", found.NodeName, found.NodeName)
		os.Exit(1)
	}
	if relay.ReadyTunnelCount < 1 {
		fmt.Fprintf(os.Stderr, "relay to %s has no authenticated ready tunnels\n", found.NodeName)
		os.Exit(1)
	}
	return found, relay
}

func outputLooksLikeRPCDeviceList(output string) bool {
	for _, device := range parseLlamaDevices(output) {
		if device.Remote {
			return true
		}
	}
	return false
}
