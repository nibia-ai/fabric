package main

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"os/signal"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/nibia-ai/fabric/internal/agentlock"
	"github.com/nibia-ai/fabric/internal/executor"
	"github.com/nibia-ai/fabric/internal/identity"
	"github.com/nibia-ai/fabric/internal/llamaruntime"
	"github.com/nibia-ai/fabric/internal/rpcrelay"
	"github.com/nibia-ai/fabric/internal/sysinfo"
	"github.com/nibia-ai/fabric/internal/types"
	"github.com/nibia-ai/fabric/internal/version"
)

const (
	headerVersion  = "X-Nibia-Version"
	headerProtocol = "X-Nibia-Protocol"
)

type collector struct {
	staticMu          sync.RWMutex
	static            sysinfo.StaticInfo
	staticLastAttempt time.Time
	preferredAddress  string

	aiMu       sync.Mutex
	aiAt       time.Time
	aiRuntimes []types.RuntimeInventory

	snapshotMu    sync.RWMutex
	snapshot      types.Node
	snapshotReady bool
}

func newCollector(preferredAddress string) *collector {
	s, err := sysinfo.Static()
	if err != nil {
		log.Printf("static telemetry partially unavailable: %v; will retry in background", err)
	}
	return &collector{static: s, staticLastAttempt: time.Now(), preferredAddress: strings.TrimSpace(preferredAddress)}
}

func (c *collector) currentStatic() sysinfo.StaticInfo {
	c.staticMu.RLock()
	defer c.staticMu.RUnlock()
	return c.static
}

func mergeStaticInfo(old, fresh sysinfo.StaticInfo) sysinfo.StaticInfo {
	if fresh.Platform != "" {
		old.Platform = fresh.Platform
	}
	if fresh.CPUModel != "" {
		old.CPUModel = fresh.CPUModel
	}
	if fresh.CPUPhysical > 0 {
		old.CPUPhysical = fresh.CPUPhysical
	}
	if fresh.CPULogical > 0 {
		old.CPULogical = fresh.CPULogical
	}
	if len(fresh.Capabilities) > 0 {
		old.Capabilities = append([]string(nil), fresh.Capabilities...)
	}
	return old
}

func (c *collector) refreshStaticIfNeeded() {
	cur := c.currentStatic()
	if cur.Platform != "" && cur.CPUModel != "" && cur.CPUPhysical > 0 && cur.CPULogical > 0 {
		return
	}

	c.staticMu.Lock()
	if !c.staticLastAttempt.IsZero() && time.Since(c.staticLastAttempt) < 30*time.Second {
		c.staticMu.Unlock()
		return
	}
	c.staticLastAttempt = time.Now()
	c.staticMu.Unlock()

	fresh, err := sysinfo.Static()
	c.staticMu.Lock()
	c.static = mergeStaticInfo(c.static, fresh)
	c.staticMu.Unlock()
	if err != nil {
		log.Printf("static telemetry retry incomplete: %v", err)
	}
}

func (c *collector) collectAI() []types.RuntimeInventory {
	c.aiMu.Lock()
	defer c.aiMu.Unlock()

	// Runtime metadata is much slower-changing than CPU/RAM telemetry.
	// Refresh the llama.cpp inventory every 30 seconds instead of probing it on
	// every 5-second heartbeat.
	if !c.aiAt.IsZero() && time.Since(c.aiAt) < 30*time.Second {
		return append([]types.RuntimeInventory(nil), c.aiRuntimes...)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 6*time.Second)
	defer cancel()

	runtimes := executor.CollectAIInventory(ctx)
	c.aiAt = time.Now()
	c.aiRuntimes = append([]types.RuntimeInventory(nil), runtimes...)

	return runtimes
}

func (c *collector) collectNode(state identity.AgentState) types.Node {
	c.refreshStaticIfNeeded()
	d, err := sysinfo.Dynamic()
	if err != nil {
		log.Printf("dynamic telemetry unavailable: %v", err)
		d.CPUUsedPct = -1
		d.MemoryPressurePct = -1
		d.MemoryStallSomePct = -1
		d.MemoryStallFullPct = -1
	}

	runtimes := c.collectAI()
	networks, preferredAddress := c.networkInventory()
	rpcWorker := executor.ManagedLlamaRPCStatus()

	return c.buildNode(state, d, runtimes, networks, preferredAddress, rpcWorker)
}

func (c *collector) seedNode(state identity.AgentState) types.Node {
	// The first heartbeat must not depend on a full telemetry refresh. Start with
	// static identity/network information and let the background collector fill
	// in CPU/RAM/runtime fields as soon as they are available.
	d := sysinfo.DynamicInfo{
		CPUUsedPct:         -1,
		Load1:              -1,
		Load5:              -1,
		Load15:             -1,
		MemoryPressurePct:  -1,
		MemoryStallSomePct: -1,
		MemoryStallFullPct: -1,
	}
	networks, preferredAddress := c.networkInventory()
	return c.buildNode(
		state,
		d,
		nil,
		networks,
		preferredAddress,
		executor.ManagedLlamaRPCStatus(),
	)
}

func (c *collector) networkInventory() ([]types.NetworkInterfaceInventory, string) {
	networks, preferredAddress := sysinfo.NetworkInventory()
	if c.preferredAddress != "" {
		var err error
		networks, preferredAddress, err = sysinfo.OverridePreferredAddress(networks, c.preferredAddress)
		if err != nil {
			log.Printf("preferred address override unavailable: %v", err)
		}
	}
	return networks, preferredAddress
}

func (c *collector) buildNode(
	state identity.AgentState,
	d sysinfo.DynamicInfo,
	runtimes []types.RuntimeInventory,
	networks []types.NetworkInterfaceInventory,
	preferredAddress string,
	rpcWorker executor.LlamaRPCWorkerStatus,
) types.Node {
	hostname, _ := os.Hostname()
	static := c.currentStatic()
	caps := sysinfo.BaseCapabilities(runtime.GOOS, runtime.GOARCH, static, d)
	caps = appendUnique(
		caps,
		"lease-v1",
		"executor-v1",
		"task-llamacpp-rpc-worker",
		"task-power-guard",
	)
	caps = appendUnique(caps, executor.CapabilitiesFromAIInventory(runtimes)...)

	return types.Node{
		ID: state.NodeID, Name: state.Name, Hostname: hostname,
		AgentVersion: version.Version, ProtocolVersion: version.ProtocolVersion,
		OS: runtime.GOOS, Arch: runtime.GOARCH, Platform: static.Platform, CPUModel: static.CPUModel,
		CPUPhysical: static.CPUPhysical, CPULogical: static.CPULogical, CPUUsedPct: d.CPUUsedPct,
		Load1: d.Load1, Load5: d.Load5, Load15: d.Load15,
		MemoryTotalMB: d.MemoryTotalMB, MemoryAvailableMB: d.MemoryAvailableMB,
		MemoryUsedMB: d.MemoryUsedMB, MemoryUsedPct: d.MemoryUsedPct,
		MemoryPressurePct: d.MemoryPressurePct, MemoryHeadroomPct: sysinfo.MemoryHeadroomPct(d),
		MemoryPressureLevel: sysinfo.MemoryPressureLevel(d), CompressedMB: d.CompressedMB,
		SwapTotalMB: d.SwapTotalMB, SwapUsedMB: d.SwapUsedMB, UptimeSeconds: d.UptimeSeconds,
		Capabilities:  caps,
		ResourceState: sysinfo.ResourceState(d), ResourceScore: sysinfo.ResourceScore(d),
		PreferredAddress: preferredAddress, NetworkInterfaces: networks,
		Runtimes:                    runtimes,
		RPCWorkerManaged:            rpcWorker.Managed,
		RPCWorkerRunning:            rpcWorker.Running,
		RPCWorkerPID:                rpcWorker.PID,
		RPCWorkerProcessCPUPercent:  rpcWorker.ProcessCPUPercent,
		RPCWorkerProcessRSSBytes:    rpcWorker.ProcessRSSBytes,
		RPCWorkerPeakCPUPercent:     rpcWorker.PeakCPUPercent,
		RPCWorkerPeakRSSBytes:       rpcWorker.PeakRSSBytes,
		RPCWorkerTelemetryStartedAt: rpcWorker.TelemetryStartedAt,
		UpdatedAt:                   time.Now().UTC(),
	}
}

func (c *collector) storeSnapshot(node types.Node) {
	c.snapshotMu.Lock()
	c.snapshot = node
	c.snapshotReady = true
	c.snapshotMu.Unlock()
}

func (c *collector) snapshotNode() (types.Node, bool) {
	c.snapshotMu.RLock()
	defer c.snapshotMu.RUnlock()
	if !c.snapshotReady {
		return types.Node{}, false
	}
	return c.snapshot, true
}

func main() {
	controllerURL := flag.String("controller", "", "secure controller URL; defaults to paired state")
	pairURL := flag.String("pair", "", "pairing URL, e.g. http://192.168.1.10:8080")
	code := flag.String("code", "", "one-time NIBIA pairing code")
	name := flag.String("name", "", "friendly node name")
	stateDir := flag.String("state-dir", identity.DefaultAgentDir(), "persistent agent identity directory")
	heartbeatInterval := flag.Duration("interval", 5*time.Second, "heartbeat interval")
	pollInterval := flag.Duration("poll-interval", 2*time.Second, "lease polling interval")
	relayAddress := flag.String("relay-address", "", "mTLS reverse relay controller address; defaults to controller host:9443")
	relayPort := flag.Int("relay-port", rpcrelay.DefaultControllerPort, "derived reverse relay controller port")
	relayPool := flag.Int("relay-pool", 4, "number of authenticated ready relay tunnels")
	preferredAddress := flag.String("preferred-address", "", "preferred local IPv4 address advertised for fabric traffic")
	flag.Parse()

	state, priv, err := identity.EnsureAgentIdentity(*stateDir, *name)
	if err != nil {
		log.Fatalf("agent identity: %v", err)
	}
	instanceLock, err := agentlock.Acquire(*stateDir)
	if err != nil {
		log.Fatalf("agent instance lock: %v", err)
	}
	defer instanceLock.Release()

	if strings.TrimSpace(*pairURL) != "" {
		if strings.TrimSpace(*code) == "" {
			log.Fatal("--code is required with --pair")
		}
		state, err = pair(state, priv, *stateDir, strings.TrimRight(*pairURL, "/"), strings.TrimSpace(*code))
		if err != nil {
			log.Fatalf("pairing failed: %v", err)
		}
		log.Printf("paired successfully with controller %s", state.ControllerFingerprint)
		log.Printf("credentials saved in %s", *stateDir)
	}

	if strings.TrimSpace(*controllerURL) != "" {
		overrideURL := strings.TrimRight(strings.TrimSpace(*controllerURL), "/")
		if state.SecureControllerURL != overrideURL {
			state.SecureControllerURL = overrideURL
			if err := identity.SaveAgentState(*stateDir, state); err != nil {
				log.Printf("warning: persist --controller override: %v", err)
			}
		}
	}
	if state.SecureControllerURL == "" {
		log.Fatal("agent is not paired; use --pair <url> --code <code>")
	}
	if !strings.HasPrefix(strings.ToLower(state.SecureControllerURL), "https://") {
		log.Fatal("NIBIA secure agents require an HTTPS/mTLS controller URL")
	}

	heartbeatClient, err := secureClient(*stateDir)
	if err != nil {
		log.Fatalf("load heartbeat mTLS credentials: %v", err)
	}
	// Heartbeats are tiny liveness messages. A long request timeout can itself
	// create a >30s liveness gap after only a couple of congested requests, so
	// fail fast and retry on the next heartbeat tick instead.
	heartbeatClient.Timeout = 3 * time.Second
	workerClient, err := secureClient(*stateDir)
	if err != nil {
		log.Fatalf("load worker mTLS credentials: %v", err)
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	// Fresh public installs should not require users to compile or manually
	// install llama.cpp. Existing development PATH installs remain a fallback;
	// otherwise the Agent fetches NIBIA's pinned, verified runtime once.
	if _, _, err := llamaruntime.ResolveBinary("ggml-rpc-server"); err != nil {
		runtimeCtx, cancelRuntime := context.WithTimeout(ctx, 20*time.Minute)
		_, ensureErr := llamaruntime.Ensure(runtimeCtx, log.Writer())
		cancelRuntime()
		if ensureErr != nil && ctx.Err() == nil {
			log.Printf("managed llama.cpp runtime unavailable: %v", ensureErr)
		}
	}
	if strings.TrimSpace(*preferredAddress) != "" {
		networks, _ := sysinfo.NetworkInventory()
		if _, _, err := sysinfo.OverridePreferredAddress(networks, strings.TrimSpace(*preferredAddress)); err != nil {
			log.Fatalf("--preferred-address: %v", err)
		}
	}
	col := newCollector(*preferredAddress)

	effectiveRelayAddress := strings.TrimSpace(*relayAddress)
	if effectiveRelayAddress == "" {
		effectiveRelayAddress, err = rpcrelay.DeriveRelayAddress(state.SecureControllerURL, *relayPort)
		if err != nil {
			log.Fatalf("derive RPC relay controller address: %v", err)
		}
	}
	certPath, keyPath, caPath := identity.AgentCredentialPaths(*stateDir)
	go func() {
		err := rpcrelay.RunAgentPool(ctx, rpcrelay.AgentConfig{
			NodeID: state.NodeID, NodeName: state.Name, RelayAddress: effectiveRelayAddress,
			CertFile: certPath, KeyFile: keyPath, CAFile: caPath, PoolSize: *relayPool,
		}, log.Printf)
		if err != nil && ctx.Err() == nil {
			log.Printf("RPC relay pool unavailable: %v", err)
		}
	}()
	defer executor.ShutdownManagedRuntimes()

	log.Printf(
		"NIBIA agent v%s protocol=%s starting: %s (%s/%s) -> %s",
		version.Version, version.ProtocolVersion, state.Name, runtime.GOOS, runtime.GOARCH, state.SecureControllerURL,
	)
	log.Printf("executor enabled: poll=%s tasks=llama-rpc,power-guard", *pollInterval)
	log.Printf("authenticated RPC reverse relay: controller=%s pool=%d", effectiveRelayAddress, *relayPool)
	if strings.TrimSpace(*preferredAddress) != "" {
		log.Printf("preferred fabric address override: %s", strings.TrimSpace(*preferredAddress))
	}

	var executing atomic.Bool

	// Publish a minimal snapshot immediately, then refresh telemetry on its own
	// loop. Heartbeat liveness must never wait for PowerShell/CIM, procfs, model
	// inventory, network inventory, or RPC telemetry to finish.
	col.storeSnapshot(col.seedNode(state))
	go telemetryLoop(ctx, col, 5*time.Second, func() types.Node {
		return col.collectNode(state)
	})

	// Heartbeats and lease execution use independent HTTP transports. Heavy
	// data-plane RPC traffic can still contend for the same physical LAN, but
	// one slow control request no longer occupies the worker poller's connection
	// pool (or vice versa).
	go heartbeatLoop(ctx, heartbeatClient, state.SecureControllerURL, col, *heartbeatInterval)
	go workerLoop(ctx, workerClient, state.SecureControllerURL, state, *pollInterval, &executing)

	<-ctx.Done()
	if err := postOffline(heartbeatClient, state.SecureControllerURL); err != nil {
		log.Printf("graceful offline notification failed: %v", err)
	} else {
		log.Printf("graceful offline notification sent")
	}
	log.Printf("NIBIA agent stopped")
}

func telemetryLoop(
	ctx context.Context,
	col *collector,
	interval time.Duration,
	collect func() types.Node,
) {
	if interval <= 0 {
		interval = 5 * time.Second
	}

	refresh := func() bool {
		resultCh := make(chan types.Node, 1)
		started := time.Now()
		go func() {
			resultCh <- collect()
		}()

		slowTimer := time.NewTimer(10 * time.Second)
		defer slowTimer.Stop()
		slowC := slowTimer.C

		for {
			select {
			case <-ctx.Done():
				return false
			case node := <-resultCh:
				col.storeSnapshot(node)
				if elapsed := time.Since(started); elapsed >= 10*time.Second {
					log.Printf("telemetry refresh recovered after %s", elapsed.Round(time.Millisecond))
				}
				return true
			case <-slowC:
				log.Printf("telemetry refresh slow after %s; heartbeat continues from last good snapshot", time.Since(started).Round(time.Millisecond))
				slowC = nil
			}
		}
	}

	if !refresh() {
		return
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if !refresh() {
				return
			}
		}
	}
}

var (
	heartbeatLogOnce sync.Once
	heartbeatLogCh   chan string
)

// heartbeatLogf is deliberately non-blocking. Agent liveness must never depend
// on terminal/stdout/stderr throughput: a stalled console writer may delay or
// drop heartbeat diagnostics, but it must not stall the heartbeat scheduler.
func heartbeatLogf(format string, args ...any) {
	heartbeatLogOnce.Do(func() {
		heartbeatLogCh = make(chan string, 64)
		go func() {
			for msg := range heartbeatLogCh {
				log.Print(msg)
			}
		}()
	})
	msg := fmt.Sprintf(format, args...)
	select {
	case heartbeatLogCh <- msg:
	default:
		// Diagnostics are best-effort. Never block liveness on logging.
	}
}

func heartbeatLoop(
	ctx context.Context,
	client *http.Client,
	controller string,
	col *collector,
	interval time.Duration,
) {
	if interval <= 0 {
		interval = 5 * time.Second
	}

	lastSuccessLog := time.Time{}
	send := func() {
		node, ok := col.snapshotNode()
		if !ok {
			heartbeatLogf("heartbeat skipped: telemetry snapshot unavailable")
			return
		}
		if err := postHeartbeat(client, controller, node); err != nil {
			heartbeatLogf("heartbeat failed: %v", err)
			return
		}
		// Keep normal output useful without making the critical heartbeat loop
		// synchronously write to a console every five seconds.
		if lastSuccessLog.IsZero() || time.Since(lastSuccessLog) >= 30*time.Second {
			cpu := "n/a"
			if node.CPUUsedPct >= 0 {
				cpu = fmt.Sprintf("%.1f%%", node.CPUUsedPct)
			}
			age := time.Since(node.UpdatedAt)
			if age < 0 {
				age = 0
			}
			heartbeatLogf("secure heartbeat: cpu=%s available_ram=%dMB state=%s score=%.2f telemetry_age=%s",
				cpu, node.MemoryAvailableMB, node.ResourceState, node.ResourceScore, age.Round(time.Second))
			lastSuccessLog = time.Now()
		}
	}

	send()
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			send()
		}
	}
}

func workerLoop(
	ctx context.Context,
	client *http.Client,
	controller string,
	state identity.AgentState,
	pollInterval time.Duration,
	executing *atomic.Bool,
) {
	ticker := time.NewTicker(pollInterval)
	defer ticker.Stop()

	poll := func() {
		if executing.Load() {
			return
		}
		assignment, found, err := fetchLease(client, controller, state.NodeID)
		if err != nil {
			log.Printf("lease poll failed: %v", err)
			return
		}
		if !found {
			return
		}
		if !executing.CompareAndSwap(false, true) {
			return
		}
		go func() {
			defer executing.Store(false)
			runAssignment(ctx, client, controller, state, assignment)
		}()
	}

	poll()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			poll()
		}
	}
}

func fetchLease(client *http.Client, controller, nodeID string) (types.LeaseAssignment, bool, error) {
	url := strings.TrimRight(controller, "/") + "/v1/leases/next?node_id=" + nodeID
	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		return types.LeaseAssignment{}, false, err
	}
	setHeaders(req)
	resp, err := client.Do(req)
	if err != nil {
		return types.LeaseAssignment{}, false, err
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusNoContent {
		return types.LeaseAssignment{}, false, nil
	}
	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return types.LeaseAssignment{}, false, fmt.Errorf("%s %s", resp.Status, strings.TrimSpace(string(b)))
	}

	var out types.LeaseAssignment
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return types.LeaseAssignment{}, false, err
	}
	return out, true, nil
}

func runAssignment(
	parent context.Context,
	client *http.Client,
	controller string,
	state identity.AgentState,
	a types.LeaseAssignment,
) {
	log.Printf(
		"lease received: job=%s name=%q lease=%s attempt=%d workload=%s expires=%s",
		a.JobID, a.JobName, a.LeaseID, a.Attempt, a.Workload.Type, a.ExpiresAt.Local().Format("15:04:05"),
	)

	var ack types.LeaseRenewResponse
	if err := secureJSON(
		client, http.MethodPost, controller+"/v1/leases/"+a.LeaseID+"/ack",
		types.LeaseAckRequest{NodeID: state.NodeID}, &ack,
	); err != nil {
		log.Printf("lease ACK failed: %v", err)
		return
	}
	log.Printf("lease ACK: %s", a.LeaseID)

	ctx, cancel := context.WithCancel(parent)
	defer cancel()

	type result struct {
		success  bool
		summary  string
		executor *types.ExecutorResult
		err      error
	}
	resultCh := make(chan result, 1)
	start := time.Now()
	executionStartedAt := start.UTC()

	go func() {
		summary, execResult, err := executor.Execute(ctx, a.Workload)
		resultCh <- result{
			success:  err == nil,
			summary:  summary,
			executor: execResult,
			err:      err,
		}
	}()

	renewTicker := time.NewTicker(5 * time.Second)
	defer renewTicker.Stop()

	for {
		select {
		case <-parent.Done():
			cancel()
			return

		case res := <-resultCh:
			duration := time.Since(start)
			summary := res.summary
			if res.err != nil {
				if errors.Is(res.err, context.Canceled) {
					log.Printf("job cancelled locally: %s", a.JobID)
					return
				}
				if summary == "" {
					summary = res.err.Error()
				}
			}

			req := types.LeaseCompleteRequest{
				NodeID:             state.NodeID,
				Success:            res.success,
				Summary:            summary,
				DurationMS:         duration.Milliseconds(),
				ExecutionStartedAt: &executionStartedAt,
				Executor:           res.executor,
			}
			var completed types.ScheduledJob
			if err := secureJSON(
				client, http.MethodPost, controller+"/v1/leases/"+a.LeaseID+"/complete",
				req, &completed,
			); err != nil {
				log.Printf("job completion failed: job=%s lease=%s err=%v", a.JobID, a.LeaseID, err)
				return
			}
			log.Printf(
				"job completed: id=%s state=%s duration=%s result=%q",
				completed.ID, completed.State, duration.Round(time.Millisecond), summary,
			)
			return

		case <-renewTicker.C:
			var renewed types.LeaseRenewResponse
			err := secureJSON(
				client, http.MethodPost, controller+"/v1/leases/"+a.LeaseID+"/renew",
				types.LeaseRenewRequest{NodeID: state.NodeID}, &renewed,
			)
			if err != nil {
				if strings.Contains(err.Error(), "409") {
					log.Printf("lease cancelled by controller: job=%s lease=%s", a.JobID, a.LeaseID)
					cancel()
					return
				}
				log.Printf("lease renewal failed: job=%s lease=%s err=%v", a.JobID, a.LeaseID, err)
				// Do not continue executing without a valid lease.
				cancel()
				return
			}
			if renewed.CancelRequested {
				log.Printf("lease cancellation requested: job=%s", a.JobID)
				cancel()
				return
			}
			log.Printf("lease renewed: %s -> %s", a.LeaseID, renewed.ExpiresAt.Local().Format("15:04:05"))
		}
	}
}

func appendUnique(values []string, additions ...string) []string {
	have := map[string]bool{}
	for _, v := range values {
		have[strings.ToLower(v)] = true
	}
	for _, v := range additions {
		if !have[strings.ToLower(v)] {
			values = append(values, v)
			have[strings.ToLower(v)] = true
		}
	}
	return values
}

func pair(state identity.AgentState, priv ed25519.PrivateKey, stateDir, pairURL, code string) (identity.AgentState, error) {
	csr, err := identity.CreateCSR(state, priv)
	if err != nil {
		return state, err
	}
	reqBody := types.PairClaimRequest{
		Code: code, NodeID: state.NodeID, Name: state.Name,
		ProtocolVersion: version.ProtocolVersion, CSRPEM: string(csr),
	}
	body, _ := json.Marshal(reqBody)
	req, err := http.NewRequest(http.MethodPost, pairURL+"/v1/pairing/claim", bytes.NewReader(body))
	if err != nil {
		return state, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set(headerVersion, version.Version)
	req.Header.Set(headerProtocol, version.ProtocolVersion)

	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return state, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return state, fmt.Errorf("controller returned %s: %s", resp.Status, strings.TrimSpace(string(b)))
	}

	var out types.PairClaimResponse
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return state, err
	}
	if out.ProtocolVersion != version.ProtocolVersion {
		return state, fmt.Errorf("paired controller returned protocol %s; expected %s", out.ProtocolVersion, version.ProtocolVersion)
	}
	fp, err := identity.FingerprintPEM([]byte(out.CACertificatePEM))
	if err != nil {
		return state, err
	}
	if fp != out.ControllerFingerprint {
		return state, fmt.Errorf("controller fingerprint mismatch during pairing")
	}

	state.SecureControllerURL = strings.TrimRight(out.SecureControllerURL, "/")
	state.ControllerFingerprint = out.ControllerFingerprint
	state.ProtocolVersion = out.ProtocolVersion
	if err := identity.SaveAgentCredentials(stateDir, state, []byte(out.CertificatePEM), []byte(out.CACertificatePEM)); err != nil {
		return state, err
	}
	return state, nil
}

func secureClient(stateDir string) (*http.Client, error) {
	certPath, keyPath, caPath := identity.AgentCredentialPaths(stateDir)
	cert, err := tls.LoadX509KeyPair(certPath, keyPath)
	if err != nil {
		return nil, err
	}
	caPEM, err := os.ReadFile(caPath)
	if err != nil {
		return nil, err
	}
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(caPEM) {
		return nil, fmt.Errorf("invalid controller CA")
	}
	tr := &http.Transport{
		TLSClientConfig: &tls.Config{
			MinVersion:   tls.VersionTLS13,
			RootCAs:      pool,
			Certificates: []tls.Certificate{cert},
		},
		MaxIdleConns:        16,
		MaxIdleConnsPerHost: 8,
		IdleConnTimeout:     30 * time.Second,
		TLSHandshakeTimeout: 6 * time.Second,
	}
	// Tolerate short LAN congestion bursts caused by large model transfers
	// without immediately declaring the control plane unhealthy.
	return &http.Client{Timeout: 15 * time.Second, Transport: tr}, nil
}

func postHeartbeat(client *http.Client, controller string, node types.Node) error {
	body, err := json.Marshal(node)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(controller, "/")+"/v1/nodes", bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	setHeaders(req)
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return fmt.Errorf("controller rejected secure heartbeat: %s %s", resp.Status, strings.TrimSpace(string(b)))
	}
	return nil
}

func postOffline(client *http.Client, controller string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(controller, "/")+"/v1/nodes/offline", nil)
	if err != nil {
		return err
	}
	setHeaders(req)
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return fmt.Errorf("controller rejected offline notification: %s %s", resp.Status, strings.TrimSpace(string(b)))
	}
	return nil
}

func secureJSON(client *http.Client, method, url string, input, output any) error {
	var body io.Reader
	if input != nil {
		b, err := json.Marshal(input)
		if err != nil {
			return err
		}
		body = bytes.NewReader(b)
	}
	req, err := http.NewRequest(method, url, body)
	if err != nil {
		return err
	}
	setHeaders(req)
	if input != nil {
		req.Header.Set("Content-Type", "application/json")
	}

	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return fmt.Errorf("%s: %s", resp.Status, strings.TrimSpace(string(b)))
	}
	if output != nil && resp.StatusCode != http.StatusNoContent {
		return json.NewDecoder(resp.Body).Decode(output)
	}
	return nil
}

func setHeaders(req *http.Request) {
	req.Header.Set(headerVersion, version.Version)
	req.Header.Set(headerProtocol, version.ProtocolVersion)
}
