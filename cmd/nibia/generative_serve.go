package main

import (
	"context"
	"fmt"
	"io"
	"math"
	"net"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/nibia-ai/fabric/internal/types"
)

// generativeServeCmd starts a persistent llama-server backed by the same
// NIBIA planner, managed RPC workers and authenticated relays used by
// run. The server binds to loopback by default so external clients
// on the Primary Node can use the built-in llama.cpp Web UI / OpenAI-style API
// without exposing the endpoint to the LAN accidentally.
func generativeServeCmd(args []string) {
	fs := newCommandFlagSet("serve", "nibia serve --model <file.gguf> [options]")
	controller := fs.String("controller", "http://127.0.0.1:8080", "Controller URL")
	node := fs.String("node", "", "single remote RPC worker node name or id; equivalent to a single-entry --nodes")
	nodes := fs.String("nodes", "", "comma-separated remote RPC worker nodes; empty means auto-discover all eligible workers")
	model := fs.String("model", "", "local GGUF model path on the Primary Node")
	ctxSize := fs.Int("ctx", 4096, "server context size")
	loadMode := fs.String("load-mode", "none", "llama.cpp load mode: none or auto")
	reserveMiB := fs.Uint64("reserve-mb", 0, "legacy global fixed memory reserve override in MiB; omitted means adaptive reservation")
	memoryReserve := fs.String("memory-reserve", "", "per-node memory reservation overrides, e.g. primary=2GiB,worker-a=20%,worker-b=768MiB")
	forceDistributed := fs.Bool("force-distributed", false, "use remote devices even when the model fits locally")
	autoStart := fs.Bool("auto-start", true, "start managed RPC workers and relays when needed")
	planningTimeout := fs.Duration("planning-timeout", 20*time.Second, "maximum time for each N-node llama.cpp device-discovery attempt")
	nodeGrace := fs.Duration("node-grace", 15*time.Second, "grace window for recently stale/OFFLINE worker capacity to recover when needed")
	autoRecover := fs.Bool("auto-recover", true, "keep SERVE alive and rebuild the distributed runtime after a selected worker reconnects")
	recoveryTimeout := fs.Duration("recovery-timeout", 5*time.Minute, "maximum time SERVE waits for selected worker capacity to recover")
	allowRuntimeMismatch := fs.Bool("allow-runtime-mismatch", false, "development escape hatch: permit incompatible llama.cpp source identities")
	host := fs.String("host", "127.0.0.1", "llama-server bind host; loopback-only in this Experimental Alpha")
	port := fs.Int("port", 8081, "llama-server port")
	verbose := fs.Bool("verbose", false, "show detailed NIBIA planning and Fabric telemetry")
	verboseRuntime := fs.Bool("verbose-runtime", false, "show raw llama-server runtime output")
	showProgress := fs.Bool("progress", true, "show distributed model-loading progress")
	openUI := fs.String("open-ui", "loading", "open Web UI: loading, ready, or never")
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
	if *ctxSize < 256 || *ctxSize > 1048576 {
		fmt.Fprintln(os.Stderr, "--ctx must be between 256 and 1048576")
		os.Exit(2)
	}
	mode := strings.ToLower(strings.TrimSpace(*loadMode))
	if mode != "none" && mode != "auto" {
		fmt.Fprintln(os.Stderr, "--load-mode must be none or auto")
		os.Exit(2)
	}
	if *planningTimeout <= 0 {
		fmt.Fprintln(os.Stderr, "--planning-timeout must be > 0")
		os.Exit(2)
	}
	if *recoveryTimeout <= 0 {
		fmt.Fprintln(os.Stderr, "--recovery-timeout must be > 0")
		os.Exit(2)
	}
	if *port < 1 || *port > 65535 {
		fmt.Fprintln(os.Stderr, "--port must be between 1 and 65535")
		os.Exit(2)
	}
	if strings.TrimSpace(*host) == "" {
		fmt.Fprintln(os.Stderr, "--host must not be empty")
		os.Exit(2)
	}
	if !serveHostIsLoopback(*host) {
		fmt.Fprintln(os.Stderr, "non-loopback SERVE is not supported in this Experimental Alpha; use --host 127.0.0.1")
		os.Exit(2)
	}
	uiMode := strings.ToLower(strings.TrimSpace(*openUI))
	if uiMode != "loading" && uiMode != "ready" && uiMode != "never" {
		fmt.Fprintln(os.Stderr, "--open-ui must be loading, ready, or never")
		os.Exit(2)
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
	// Keep the OpenAI-compatible model ID and the product-facing model label
	// identical. NIBIA is the serving provider/runtime, not part of model identity.
	modelID := defaultNibiaModelID(absoluteModel)

	serveGuard, err := acquireServeInstanceGuard(*host, *port, absoluteModel)
	if err != nil {
		fmt.Fprintf(os.Stderr, "serve: %v\n", err)
		os.Exit(1)
	}
	defer serveGuard.Close()

	// Fail early when another process already owns the requested local port.
	ln, err := net.Listen("tcp", net.JoinHostPort(*host, strconv.Itoa(*port)))
	if err != nil {
		fmt.Fprintf(os.Stderr, "serve endpoint unavailable: %v\n", err)
		os.Exit(1)
	}
	_ = ln.Close()

	interruptCtx, stopSignals := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stopSignals()

	llamaCLI, err := resolveLlamaBinaryAuto(context.Background(), "llama-cli")
	if err != nil {
		fmt.Fprintln(os.Stderr, "llama-cli runtime unavailable (required for NIBIA device preflight)")
		os.Exit(1)
	}
	llamaServer, err := resolveLlamaBinaryAuto(context.Background(), "llama-server")
	if err != nil {
		fmt.Fprintln(os.Stderr, "llama-server runtime unavailable")
		os.Exit(1)
	}
	serverRuntime := llamaVersionLine(llamaServer)

	fmt.Println("NIBIA N-node Distributed Inference Server")
	fmt.Println("Lifecycle: SERVE")
	fmt.Println()
	fmt.Println("[1/4] Preparing fabric")

	// Inspect the Primary before touching remote workers. A local-fit model
	// should not create RPC workers, authenticated tunnels, or remote jobs.
	summary, err := getGenerativeFabricSummary(*controller)
	if err != nil {
		fmt.Fprintf(os.Stderr, "serve: read fabric: %v\n", err)
		os.Exit(1)
	}
	localNode, ok := localGenerativeNode(summary)
	if !ok || localNode.NodeID == "" {
		fmt.Fprintln(os.Stderr, "serve: Primary Node could not be identified in the fabric")
		os.Exit(1)
	}
	localOut, localErr := runLocalDeviceDiscoveryWithProgress(interruptCtx, llamaCLI, *planningTimeout)
	if interruptCtx.Err() != nil {
		fmt.Fprintln(os.Stderr, "\nNIBIA planning cancelled")
		os.Exit(130)
	}
	if localErr != nil {
		fmt.Fprintf(os.Stderr, "local llama.cpp device preflight failed: %v\n", localErr)
		if strings.TrimSpace(localOut) != "" {
			fmt.Fprintln(os.Stderr, localOut)
		}
		os.Exit(1)
	}
	localDevices := parseLlamaDevices(localOut)
	localPlan, fitsLocal, err := planLocalGenerativeRunNWithPolicy(info.Size(), reservePolicy, localNode, localDevices)
	if err != nil {
		fmt.Fprintf(os.Stderr, "local generative planning failed: %v\n", err)
		os.Exit(1)
	}

	plan := localPlan
	var activeNodes []types.GenerativeNodeCapability
	var relays []types.GenerativeRelayStatus
	var remainingNodes []types.GenerativeNodeCapability
	var fabricCleanupOnce sync.Once
	cleanupFabric := func() {
		fabricCleanupOnce.Do(func() {
			if !*autoStart {
				return
			}
			for _, warning := range cleanupRemoteFabric(*controller, activeNodes) {
				fmt.Fprintf(os.Stderr, "fabric cleanup warning: %s\n", warning)
			}
		})
	}

	if fitsLocal && !*forceDistributed {
		fmt.Println("      Remote workers: not required (model fits Primary safely)")
		fmt.Println("      ✓ Local fabric ready")
		if serverRuntime != "" {
			fmt.Printf("      llama.cpp runtime: %s\n", serverRuntime)
		}
	} else {
		remoteNodes, refreshedLocal, totalCandidates, recoveringCandidates, err := resolveGenerativeCandidatesForCapacityWithGracePolicy(
			interruptCtx,
			*controller, *node, *nodes,
			*nodeGrace,
			reservePolicy, localPlan.AggregateMiB, localPlan.ModelMiB,
			*forceDistributed,
		)
		if err != nil {
			fmt.Fprintf(os.Stderr, "serve: %v\n", err)
			os.Exit(1)
		}
		if refreshedLocal.NodeID != "" {
			localNode = refreshedLocal
			localPlan, _, err = planLocalGenerativeRunNWithPolicy(info.Size(), reservePolicy, localNode, localDevices)
			if err != nil {
				fmt.Fprintf(os.Stderr, "local generative planning failed: %v\n", err)
				os.Exit(1)
			}
		}
		fmt.Printf("      Candidate workers: %d\n", totalCandidates)
		for _, n := range remoteNodes {
			addr := n.PreferredAddress
			if addr == "" {
				addr = "address unknown"
			}
			fmt.Printf("        %-18s %s/%s  %s  RAM %.1f/%.1f GiB\n", n.NodeName, n.OS, n.Arch, addr, float64(n.MemoryAvailableMB)/1024, float64(n.MemoryTotalMB)/1024)
		}
		fmt.Printf("      Readiness:        %d/%d candidates fresh\n", len(remoteNodes), totalCandidates)
		if recoveringCandidates > 0 {
			fmt.Printf("      Recovering:       %d candidate(s) not required by the current safe plan\n", recoveringCandidates)
		}

		activeNodes, remainingNodes = selectRemoteNodesForCapacityWithPolicy(remoteNodes, reservePolicy, localPlan.AggregateMiB, localPlan.ModelMiB, *forceDistributed)
		if len(activeNodes) == 0 {
			fmt.Fprintln(os.Stderr, "serve: no remote worker can be activated for this execution plan")
			os.Exit(1)
		}
		fmt.Printf("      Activating workers: %d of %d fresh\n", len(activeNodes), len(remoteNodes))
		relays, err = prepareRemoteFabric(*controller, activeNodes, *autoStart, 55052)
		if err != nil {
			fmt.Fprintf(os.Stderr, "prepare N-node fabric: %v\n", err)
			os.Exit(1)
		}
		for i, r := range relays {
			if *verbose {
				fmt.Printf("      %-18s RPC READY %s (%d tunnels)\n", activeNodes[i].NodeName, r.LocalEndpoint, r.ReadyTunnelCount)
			} else {
				fmt.Printf("      %-18s RPC READY\n", activeNodes[i].NodeName)
			}
		}
		if !*allowRuntimeMismatch {
			if err := validateRuntimeParity(serverRuntime, activeNodes); err != nil {
				fmt.Fprintf(os.Stderr, "runtime compatibility check failed: %v\n", err)
				cleanupFabric()
				os.Exit(1)
			}
		}
		fmt.Println("      ✓ Fabric ready")
		fmt.Println("      RPC tensor cache: enabled (persistent, worker-local)")
		if serverRuntime != "" {
			fmt.Printf("      llama.cpp runtime: %s\n", serverRuntime)
			fmt.Println("      Runtime parity:    compatible (version + commit match)")
		}
	}

	fmt.Println()
	fmt.Println("[2/4] Planning execution")
	if !(fitsLocal && !*forceDistributed) {
		discoverAndPlan := func() (automaticGenerativePlanN, error) {
			rpcEndpoints := relayEndpoints(relays)
			deviceOut, deviceErr := runDeviceDiscoveryWithProgress(interruptCtx, llamaCLI, rpcEndpoints, *planningTimeout)
			if interruptCtx.Err() != nil {
				return automaticGenerativePlanN{}, interruptCtx.Err()
			}
			devices := parseLlamaDevices(deviceOut)
			if deviceErr != nil || !allRemoteDevicesVisible(devices, relays) {
				fmt.Println("      ⚠ N-node RPC discovery did not complete cleanly; recovering once.")
				if err := restartRemoteWorkers(*controller, activeNodes); err != nil {
					return automaticGenerativePlanN{}, fmt.Errorf("RPC worker recovery failed: %w", err)
				}
				if err := waitForContext(interruptCtx, 500*time.Millisecond); err != nil {
					return automaticGenerativePlanN{}, err
				}
				deviceOut, deviceErr = runDeviceDiscoveryWithProgress(interruptCtx, llamaCLI, rpcEndpoints, *planningTimeout)
				devices = parseLlamaDevices(deviceOut)
			}
			if deviceErr != nil || !allRemoteDevicesVisible(devices, relays) {
				return automaticGenerativePlanN{}, fmt.Errorf("N-node llama.cpp RPC preflight failed: one or more activated remote devices are not visible")
			}
			remoteDevices := attachRemoteDevices(devices, activeNodes, relays)
			return planAutomaticGenerativeRunNWithPolicy(info.Size(), reservePolicy, *forceDistributed, localNode, devices, remoteDevices)
		}

		plan, err = discoverAndPlan()
		if err != nil {
			fmt.Fprintf(os.Stderr, "serve planning failed: %v\n", err)
			cleanupFabric()
			os.Exit(1)
		}
		// Agent telemetry is deliberately conservative, but runtime-free memory
		// can still be lower. Expand lazily one worker at a time only if the
		// authoritative llama.cpp plan says the active subset is insufficient.
		for plan.Classification == "INSUFFICIENT-FABRIC-CAPACITY" && len(remainingNodes) > 0 {
			next := remainingNodes[0]
			remainingNodes = remainingNodes[1:]
			fmt.Printf("      ⚠ Active subset is short on runtime capacity; adding %s\n", next.NodeName)
			newRelay, prepErr := prepareRemoteFabric(*controller, []types.GenerativeNodeCapability{next}, *autoStart, 55052+len(relays))
			if prepErr != nil {
				fmt.Fprintf(os.Stderr, "expand N-node fabric: %v\n", prepErr)
				cleanupFabric()
				os.Exit(1)
			}
			activeNodes = append(activeNodes, next)
			relays = append(relays, newRelay...)
			if *verbose {
				fmt.Printf("      %-18s RPC READY %s (%d tunnels)\n", next.NodeName, newRelay[0].LocalEndpoint, newRelay[0].ReadyTunnelCount)
			} else {
				fmt.Printf("      %-18s RPC READY\n", next.NodeName)
			}
			if !*allowRuntimeMismatch {
				if err := validateRuntimeParity(serverRuntime, activeNodes); err != nil {
					fmt.Fprintf(os.Stderr, "runtime compatibility check failed: %v\n", err)
					cleanupFabric()
					os.Exit(1)
				}
			}
			plan, err = discoverAndPlan()
			if err != nil {
				fmt.Fprintf(os.Stderr, "serve planning failed after fabric expansion: %v\n", err)
				cleanupFabric()
				os.Exit(1)
			}
		}
	}

	printAutomaticPlanN(localNode, absoluteModel, info.Size(), *ctxSize, 0, mode, "SERVE", plan, *verbose)
	if plan.Classification == "INSUFFICIENT-FABRIC-CAPACITY" {
		fmt.Fprintln(os.Stderr, "\nNIBIA refused launch: selected N-node fabric capacity is insufficient for this model")
		cleanupFabric()
		os.Exit(1)
	}

	serveStarted := time.Now()
	recoveryCount := 0
	uiOpened := false

	for {
		powerGuards, guardedNodes, powerWarnings := acquireExecutionPowerGuards(*controller, plan, activeNodes)
		totalSelected := planNodeCount(plan)

		fmt.Println()
		if recoveryCount == 0 {
			fmt.Println("[3/4] Starting persistent server")
		} else {
			fmt.Printf("[RECOVERY %d] Restoring persistent server\n", recoveryCount)
		}
		fmt.Printf("      Power guard:     active on %d/%d selected nodes\n", guardedNodes, totalSelected)
		for _, warning := range powerWarnings {
			fmt.Printf("      ⚠ %s\n", warning)
		}
		serveArgs := buildAutomaticLlamaServerArgsN(absoluteModel, modelID, *ctxSize, mode, *host, *port, plan)
		if *verboseRuntime {
			fmt.Printf("      llama-server: %s\n", llamaServer)
		}
		fmt.Printf("      Web UI:         http://%s:%d/\n", displayServeHost(*host), *port)
		fmt.Printf("      OpenAI API base: http://%s:%d/v1\n", displayServeHost(*host), *port)
		fmt.Printf("      Open UI:         %s\n", uiMode)
		if *autoRecover && plan.Distributed {
			fmt.Printf("      Recovery:        automatic (wait up to %s for selected worker capacity)\n", recoveryTimeout.String())
		}
		fmt.Println("      Model remains loaded until Ctrl+C.")
		fmt.Println()
		if recoveryCount == 0 {
			fmt.Println("[4/4] Loading model")
		} else {
			fmt.Println("      Reloading model after fabric recovery")
		}

		loadTracker := newServeLoadTracker()
		runtimeRelayBaselines := map[string]types.GenerativeRelayStatus{}
		if plan.Distributed {
			runtimeRelayBaselines = serveRelayBaselines(*controller, plan)
		}
		cmd := exec.CommandContext(interruptCtx, llamaServer, serveArgs...)
		cmd.Stdin = os.Stdin
		cmd.Stdout = serveTrackingWriter{dst: os.Stdout, tracker: loadTracker, verbose: *verboseRuntime}
		cmd.Stderr = serveTrackingWriter{dst: os.Stderr, tracker: loadTracker, verbose: *verboseRuntime}
		runtimeStarted := time.Now()
		if err := cmd.Start(); err != nil {
			for _, warning := range cleanupRemoteFabric(*controller, activeNodes) {
				fmt.Fprintf(os.Stderr, "fabric cleanup warning: %s\n", warning)
			}
			for _, warning := range powerGuards.Release() {
				fmt.Fprintf(os.Stderr, "power guard cleanup warning: %s\n", warning)
			}
			fmt.Fprintf(os.Stderr, "start llama-server: %v\n", err)
			os.Exit(1)
		}

		uiURL := fmt.Sprintf("http://%s:%d/", displayServeHost(*host), *port)
		if !uiOpened {
			if uiMode == "loading" {
				uiOpened = true
				go openUIWhenReachable(interruptCtx, uiURL)
			} else if uiMode == "ready" {
				uiOpened = true
				go func(tracker *serveLoadTracker) {
					select {
					case <-tracker.Done():
						_ = openURL(uiURL)
					case <-interruptCtx.Done():
					}
				}(loadTracker)
			}
		}

		if *showProgress {
			progressOut := io.Writer(os.Stdout)
			if *verboseRuntime {
				progressOut = io.Discard
			}
			if plan.Distributed {
				target := plannedRemoteTargetBytes(info.Size(), plan)
				fmt.Printf("      Remote tensors planned: %.2f GiB\n", float64(target)/(1024*1024*1024))
				fmt.Println("      RPC tensor cache:       resolving worker-local reuse")
				go monitorServeDistributedLoad(*controller, runtimeRelayBaselines, target, loadTracker.Done(), interruptCtx.Done(), newProgressRenderer(progressOut))
			} else {
				go monitorServeLocalLoad(loadTracker.Done(), interruptCtx.Done(), newProgressRenderer(progressOut))
			}
		}
		if recoveryCount == 0 {
			go func() {
				<-interruptCtx.Done()
				fmt.Fprintln(os.Stderr, "\nStopping NIBIA server; cleaning up runtime resources...")
			}()
		}

		err = cmd.Wait()
		modelReady := loadTracker.Ready()
		var runtimeRelayDeltas []string
		if *verbose && plan.Distributed {
			runtimeRelayDeltas = selectedRelayStatusDeltas(*controller, plan, runtimeRelayBaselines)
		}
		fabricCleanupWarnings := cleanupRemoteFabric(*controller, activeNodes)
		for _, warning := range fabricCleanupWarnings {
			fmt.Fprintf(os.Stderr, "fabric cleanup warning: %s\n", warning)
		}
		powerCleanupWarnings := powerGuards.Release()
		for _, warning := range powerCleanupWarnings {
			fmt.Fprintf(os.Stderr, "cleanup warning: power guard: %s\n", warning)
		}
		if interruptCtx.Err() != nil {
			fmt.Println("\n✓ NIBIA server stopped")
			fmt.Println("Lifecycle:        SERVE")
			fmt.Printf("Execution mode:   %s\n", map[bool]string{true: "DISTRIBUTED", false: "LOCAL"}[plan.Distributed])
			fmt.Printf("Selected nodes:   %d\n", planNodeCount(plan))
			fmt.Printf("Serve duration:   %s\n", formatClock(time.Since(serveStarted)))
			if len(fabricCleanupWarnings) == 0 && len(powerCleanupWarnings) == 0 {
				fmt.Println("Runtime cleanup:  complete")
			} else {
				fmt.Println("Runtime cleanup:  completed with warnings")
			}
			if *verbose && len(runtimeRelayDeltas) > 0 {
				fmt.Println("\nPer-node RPC contribution:")
				for _, line := range runtimeRelayDeltas {
					fmt.Println(line)
				}
			}
			return
		}
		if err == nil {
			fmt.Println("\n✓ NIBIA server exited")
			return
		}

		lostWorkers := unavailableGenerativeNodeNames(*controller, activeNodes)
		if plan.Distributed && len(lostWorkers) > 0 {
			if !modelReady {
				fmt.Fprintf(os.Stderr, "\nserve runtime lost worker capacity before model ready: %s\n", strings.Join(lostWorkers, ", "))
			} else {
				fmt.Fprintf(os.Stderr, "\nserve interrupted: distributed worker unavailable: %s\n", strings.Join(lostWorkers, ", "))
				fmt.Fprintln(os.Stderr, "The active request/runtime cannot continue safely after worker loss.")
			}
			if !*autoRecover {
				fmt.Fprintln(os.Stderr, "Automatic service recovery is disabled; restart the worker and run SERVE again.")
				os.Exit(1)
			}

			recoveryCount++
			fmt.Println()
			fmt.Printf("↻ RECOVERING NIBIA (attempt %d)\n", recoveryCount)
			fmt.Printf("  Worker offline: %s\n", strings.Join(lostWorkers, ", "))
			fmt.Println("  Restart the NIBIA Agent on the affected computer if it is not running.")
			fmt.Printf("  NIBIA will recover automatically when it reconnects (timeout %s).\n", recoveryTimeout.String())
			fmt.Println("  The interrupted request is not replayed automatically.")

			recoveredNodes, recoveredRelays, recoveredPlan, recoverErr := recoverDistributedServeFabric(
				interruptCtx,
				*controller,
				activeNodes,
				localNode,
				localDevices,
				llamaCLI,
				info.Size(),
				reservePolicy,
				*forceDistributed,
				*autoStart,
				*allowRuntimeMismatch,
				serverRuntime,
				*planningTimeout,
				*recoveryTimeout,
			)
			if interruptCtx.Err() != nil {
				fmt.Println("\n✓ NIBIA server stopped during recovery")
				return
			}
			if recoverErr != nil {
				fmt.Fprintf(os.Stderr, "\nserve recovery failed: %v\n", recoverErr)
				os.Exit(1)
			}
			activeNodes = recoveredNodes
			relays = recoveredRelays
			plan = recoveredPlan
			fmt.Println("✓ NIBIA fabric recovered; restoring model and API service.")
			continue
		}

		fmt.Fprintf(os.Stderr, "\nserve runtime exited after %s: %v\n", formatClock(time.Since(runtimeStarted)), err)
		os.Exit(1)
	}
}

func buildAutomaticLlamaServerArgsN(model, modelID string, ctxSize int, loadMode, host string, port int, plan automaticGenerativePlanN) []string {
	args := []string{"-m", model, "-a", modelID, "-c", strconv.Itoa(ctxSize), "--host", host, "--port", strconv.Itoa(port), "--cors-origins", "localhost"}
	if !plan.Distributed {
		if plan.Selected[0].Device.CPUOnly {
			return append(args, "-dev", "none", "-ngl", "0", "-fit", "off", "--load-mode", loadMode)
		}
		return append(args, "-dev", plan.Selected[0].Device.ID, "-ngl", "all", "-fit", "off", "--load-mode", loadMode)
	}
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
		args = append(args, "-dev", strings.Join(deviceIDs, ","), "-sm", "layer", "-ts", strings.Join(split, ","), "-ngl", "auto", "-fit", "on")
		if len(fitTargets) > 0 {
			args = append(args, "-fitt", strings.Join(fitTargets, ","))
		}
		return append(args, "--load-mode", loadMode)
	}
	return append(args, "-dev", strings.Join(deviceIDs, ","), "-sm", "layer", "-ts", strings.Join(split, ","), "-ngl", "all", "-fit", "off", "--load-mode", loadMode)
}

type serveRecoverySnapshot struct {
	Nodes  []types.GenerativeNodeCapability
	Issues []string
}

func serveRecoverySnapshotFromSummary(summary types.GenerativeFabricSummary, selected []types.GenerativeNodeCapability, now time.Time) serveRecoverySnapshot {
	byID := make(map[string]types.GenerativeNodeCapability, len(summary.Nodes))
	for _, n := range summary.Nodes {
		byID[n.NodeID] = n
	}
	out := serveRecoverySnapshot{Nodes: make([]types.GenerativeNodeCapability, 0, len(selected))}
	for _, prior := range selected {
		current, ok := byID[prior.NodeID]
		name := strings.TrimSpace(prior.NodeName)
		if name == "" {
			name = prior.NodeID
		}
		if !ok {
			out.Issues = append(out.Issues, fmt.Sprintf("%s: node is not present in controller inventory", name))
			continue
		}
		if issue := generativeNodeFreshnessIssue(current, now); issue != "" {
			out.Issues = append(out.Issues, fmt.Sprintf("%s: %s", name, issue))
			continue
		}
		out.Nodes = append(out.Nodes, current)
	}
	return out
}

func refreshedServeRecoveryNodes(controller string, selected []types.GenerativeNodeCapability, now time.Time) (serveRecoverySnapshot, error) {
	summary, err := getGenerativeFabricSummary(controller)
	if err != nil {
		return serveRecoverySnapshot{}, err
	}
	return serveRecoverySnapshotFromSummary(summary, selected, now), nil
}

func recoveryWorkerLabel(selected []types.GenerativeNodeCapability, issues []string) string {
	if len(selected) == 1 {
		return selected[0].NodeName
	}
	// Recovery currently preserves the selected worker set. Prefer a concise
	// actionable label over repeating low-level readiness details.
	for _, issue := range issues {
		for _, node := range selected {
			if strings.Contains(issue, node.NodeName) {
				return node.NodeName
			}
		}
	}
	return "affected worker"
}

func recoverDistributedServeFabric(
	ctx context.Context,
	controller string,
	selected []types.GenerativeNodeCapability,
	localNode types.GenerativeNodeCapability,
	localDevices []llamaDevice,
	llamaCLI string,
	modelBytes int64,
	reservePolicy memoryReservePolicy,
	forceDistributed bool,
	autoStart bool,
	allowRuntimeMismatch bool,
	serverRuntime string,
	planningTimeout time.Duration,
	recoveryTimeout time.Duration,
) ([]types.GenerativeNodeCapability, []types.GenerativeRelayStatus, automaticGenerativePlanN, error) {
	started := time.Now()
	deadline := started.Add(recoveryTimeout)
	lastStatus := ""
	lastPrinted := time.Time{}

	for {
		if err := ctx.Err(); err != nil {
			return nil, nil, automaticGenerativePlanN{}, err
		}
		if time.Now().After(deadline) {
			if lastStatus == "" {
				lastStatus = "selected worker capacity did not become ready"
			}
			return nil, nil, automaticGenerativePlanN{}, fmt.Errorf("selected worker capacity did not recover within %s (%s)", recoveryTimeout, lastStatus)
		}

		snap, err := refreshedServeRecoveryNodes(controller, selected, time.Now().UTC())
		if err != nil {
			lastStatus = fmt.Sprintf("controller inventory unavailable: %v", err)
			if lastPrinted.IsZero() || time.Since(lastPrinted) >= 10*time.Second {
				fmt.Printf("  … %s\n", lastStatus)
				lastPrinted = time.Now()
			}
			if err := waitForContext(ctx, time.Second); err != nil {
				return nil, nil, automaticGenerativePlanN{}, err
			}
			continue
		}
		if len(snap.Issues) > 0 || len(snap.Nodes) != len(selected) {
			lastStatus = strings.Join(snap.Issues, "; ")
			if lastStatus == "" {
				lastStatus = "selected worker set is incomplete"
			}
			if lastPrinted.IsZero() || time.Since(lastPrinted) >= 10*time.Second {
				fmt.Printf("  ↻ Still waiting for %s... %s\n", recoveryWorkerLabel(selected, snap.Issues), time.Since(started).Round(time.Second))
				lastPrinted = time.Now()
			}
			if err := waitForContext(ctx, time.Second); err != nil {
				return nil, nil, automaticGenerativePlanN{}, err
			}
			continue
		}

		fmt.Printf("  ✓ Worker reconnected after %s\n", time.Since(started).Round(100*time.Millisecond))
		fmt.Println("  ↻ Recovering NIBIA runtime...")
		relays, err := prepareRemoteFabric(controller, snap.Nodes, autoStart, 55052)
		if err != nil {
			lastStatus = fmt.Sprintf("RPC fabric restore failed: %v", err)
			_ = cleanupRemoteFabric(controller, snap.Nodes)
			if lastPrinted.IsZero() || time.Since(lastPrinted) >= 10*time.Second {
				fmt.Printf("  … %s\n", lastStatus)
				lastPrinted = time.Now()
			}
			if err := waitForContext(ctx, time.Second); err != nil {
				return nil, nil, automaticGenerativePlanN{}, err
			}
			continue
		}
		if !allowRuntimeMismatch {
			if err := validateRuntimeParity(serverRuntime, snap.Nodes); err != nil {
				_ = cleanupRemoteFabric(controller, snap.Nodes)
				return nil, nil, automaticGenerativePlanN{}, fmt.Errorf("runtime compatibility check failed during recovery: %w", err)
			}
		}

		rpcEndpoints := relayEndpoints(relays)
		deviceOut, deviceErr := runDeviceDiscoveryWithProgress(ctx, llamaCLI, rpcEndpoints, planningTimeout)
		if deviceErr != nil {
			_ = cleanupRemoteFabric(controller, snap.Nodes)
			lastStatus = fmt.Sprintf("RPC device discovery failed: %v", deviceErr)
			if err := waitForContext(ctx, time.Second); err != nil {
				return nil, nil, automaticGenerativePlanN{}, err
			}
			continue
		}
		devices := parseLlamaDevices(deviceOut)
		if !allRemoteDevicesVisible(devices, relays) {
			_ = cleanupRemoteFabric(controller, snap.Nodes)
			lastStatus = "one or more restored RPC devices are not visible"
			if err := waitForContext(ctx, time.Second); err != nil {
				return nil, nil, automaticGenerativePlanN{}, err
			}
			continue
		}
		remoteDevices := attachRemoteDevices(devices, snap.Nodes, relays)
		currentLocal := localNode
		if summary, summaryErr := getGenerativeFabricSummary(controller); summaryErr == nil {
			if refreshedLocal, ok := localGenerativeNode(summary); ok && refreshedLocal.NodeID != "" {
				currentLocal = refreshedLocal
			}
		}
		plan, err := planAutomaticGenerativeRunNWithPolicy(modelBytes, reservePolicy, forceDistributed, currentLocal, devices, remoteDevices)
		if err != nil {
			_ = cleanupRemoteFabric(controller, snap.Nodes)
			return nil, nil, automaticGenerativePlanN{}, fmt.Errorf("recovery planning failed: %w", err)
		}
		if plan.Classification == "INSUFFICIENT-FABRIC-CAPACITY" {
			_ = cleanupRemoteFabric(controller, snap.Nodes)
			lastStatus = "restored workers are fresh but safe runtime capacity is still insufficient"
			if lastPrinted.IsZero() || time.Since(lastPrinted) >= 10*time.Second {
				fmt.Printf("  … %s\n", lastStatus)
				lastPrinted = time.Now()
			}
			if err := waitForContext(ctx, 2*time.Second); err != nil {
				return nil, nil, automaticGenerativePlanN{}, err
			}
			continue
		}
		return snap.Nodes, relays, plan, nil
	}
}

type serveLoadTracker struct {
	mu   sync.Mutex
	tail string
	once sync.Once
	done chan struct{}
}

func newServeLoadTracker() *serveLoadTracker {
	return &serveLoadTracker{done: make(chan struct{})}
}

func (t *serveLoadTracker) Observe(p []byte) {
	if t == nil {
		return
	}
	t.mu.Lock()
	text := t.tail + string(p)
	if len(text) > 8192 {
		text = text[len(text)-8192:]
	}
	t.tail = text
	loaded := strings.Contains(strings.ToLower(text), "model loaded")
	t.mu.Unlock()
	if loaded {
		t.once.Do(func() { close(t.done) })
	}
}

func (t *serveLoadTracker) Done() <-chan struct{} {
	if t == nil {
		ch := make(chan struct{})
		close(ch)
		return ch
	}
	return t.done
}

func (t *serveLoadTracker) Ready() bool {
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

type serveTrackingWriter struct {
	dst     io.Writer
	tracker *serveLoadTracker
	verbose bool
}

func (w serveTrackingWriter) Write(p []byte) (int, error) {
	if w.tracker != nil {
		w.tracker.Observe(p)
	}
	if !w.verbose {
		return len(p), nil
	}
	return w.dst.Write(p)
}

func serveRelayBaselines(controller string, plan automaticGenerativePlanN) map[string]types.GenerativeRelayStatus {
	out := map[string]types.GenerativeRelayStatus{}
	for _, d := range plan.Selected {
		if d.Local || d.NodeID == "" {
			continue
		}
		if st, err := getGenerativeRelayStatus(controller, d.NodeID); err == nil {
			out[d.NodeID] = st
		}
	}
	return out
}

func monitorServeDistributedLoad(controller string, baselines map[string]types.GenerativeRelayStatus, targetRemoteBytes uint64, loadDone, processDone <-chan struct{}, renderer *progressRenderer) {
	start := time.Now()
	lastAt := start
	var lastTotal uint64
	var ema float64
	current := func() uint64 {
		var total uint64
		for id, base := range baselines {
			st, err := getGenerativeRelayStatus(controller, id)
			if err != nil || st.BytesToWorker < base.BytesToWorker {
				continue
			}
			total += st.BytesToWorker - base.BytesToWorker
		}
		return total
	}
	finish := func() {
		transferred := current()
		d := time.Since(start)
		avg := float64(transferred) / math.Max(d.Seconds(), 0.001)
		renderer.Done(fmt.Sprintf("      Relay traffic to workers: %.2f GiB  avg %s  elapsed %s", float64(transferred)/(1024*1024*1024), formatBytesRate(avg), formatClock(d)))
		if targetRemoteBytes > 0 {
			fmt.Printf("      Remote tensor payload planned: %.2f GiB; relay traffic observed: %.2f GiB\n", float64(targetRemoteBytes)/(1024*1024*1024), float64(transferred)/(1024*1024*1024))
			if transferred > targetRemoteBytes {
				fmt.Println("      Relay traffic includes protocol/runtime overhead and is not a tensor-payload byte count.")
			}
			if transferred < targetRemoteBytes {
				avoided := targetRemoteBytes - transferred
				fmt.Printf("      RPC cache/reuse effect: at least ~%.2f GiB of planned tensor payload avoided relay transfer\n", float64(avoided)/(1024*1024*1024))
			}
		}
		fmt.Printf("      ✓ Model ready in %s — serving requests\n", formatClock(d))
	}
	if targetRemoteBytes == 0 || len(baselines) == 0 {
		monitorServeLocalLoad(loadDone, processDone, renderer)
		return
	}
	ticker := time.NewTicker(distributedLoadProgressInterval)
	defer ticker.Stop()
	step := 0
	for {
		select {
		case <-loadDone:
			finish()
			return
		case <-processDone:
			return
		case now := <-ticker.C:
			transferred := current()
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
			// Do not divide by planned remote tensors here. Persistent RPC cache
			// can make actual network transfer dramatically smaller, so a percent
			// such as 0.25/6.67 GiB is not load progress and is misleading.
			renderer.Update(formatCachedDistributedLoadProgress(step, transferred, ema, now.Sub(start)))
			step++
			lastAt, lastTotal = now, transferred
		}
	}
}

func monitorServeLocalLoad(loadDone, processDone <-chan struct{}, renderer *progressRenderer) {
	start := time.Now()
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	step := 0
	for {
		select {
		case <-loadDone:
			d := time.Since(start)
			renderer.Done(fmt.Sprintf("      Loading local model       [%s] READY  elapsed %s", renderProgressBar(100, 24), formatClock(d)))
			fmt.Printf("      ✓ Model ready in %s — serving requests\n", formatClock(d))
			return
		case <-processDone:
			return
		case <-ticker.C:
			step = (step + 1) % 24
			r := []rune(strings.Repeat("░", 24))
			r[step] = '█'
			renderer.Update(fmt.Sprintf("      Loading local model       [%s] loading  elapsed %s", string(r), formatClock(time.Since(start))))
		}
	}
}

func defaultNibiaModelID(model string) string {
	name := cleanModelDisplayName(model)
	if name == "" {
		return "model"
	}
	return name
}

func openUIWhenReachable(ctx context.Context, url string) {
	client := &http.Client{Timeout: 700 * time.Millisecond}
	ticker := time.NewTicker(500 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			resp, err := client.Get(url)
			if err == nil {
				_ = resp.Body.Close()
				if resp.StatusCode < 500 {
					_ = openURL(url)
					return
				}
			}
		}
	}
}

func openURL(url string) error {
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		cmd = exec.Command("open", url)
	case "windows":
		cmd = exec.Command("rundll32", "url.dll,FileProtocolHandler", url)
	default:
		cmd = exec.Command("xdg-open", url)
	}
	return cmd.Start()
}

func serveHostIsLoopback(host string) bool {
	h := strings.TrimSpace(strings.ToLower(host))
	if h == "localhost" || h == "127.0.0.1" || h == "::1" {
		return true
	}
	ip := net.ParseIP(strings.Trim(h, "[]"))
	return ip != nil && ip.IsLoopback()
}

func displayServeHost(host string) string {
	h := strings.TrimSpace(host)
	if h == "0.0.0.0" || h == "::" {
		return "127.0.0.1"
	}
	return h
}
