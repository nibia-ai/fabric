package main

import (
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"os"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/nibia-ai/fabric/internal/executor"
	"github.com/nibia-ai/fabric/internal/types"
)

func fabricCmd(args []string) {
	if len(args) < 1 {
		fabricUsage()
		return
	}
	switch strings.ToLower(strings.TrimSpace(args[0])) {
	case "help", "--help", "-h":
		fabricUsage()
	case "status":
		generativeFabricCmd(args[1:])
	case "worker":
		generativeWorkerCmd(args[1:])
	case "relay":
		generativeRelayCmd(args[1:])
	case "devices":
		generativeDevicesCmd(args[1:])
	default:
		fmt.Fprintf(os.Stderr, "unknown fabric action: %s\n", args[0])
		os.Exit(2)
	}
}

func fabricUsage() {
	fmt.Println("NIBIA Fabric advanced controls")
	fmt.Println()
	fmt.Println("Usage:")
	fmt.Println("  nibia fabric status [options]")
	fmt.Println("  nibia fabric devices [options]")
	fmt.Println("  nibia fabric worker start|status|stop --node <name-or-id>")
	fmt.Println("  nibia fabric relay start|status|probe|stop --node <name-or-id>")
}

func generativeFabricCmd(args []string) {
	fs := newCommandFlagSet("fabric status", "nibia fabric status [options]")
	controller := fs.String("controller", "http://127.0.0.1:8080", "Controller URL")
	_ = fs.Parse(args)

	var summary types.GenerativeFabricSummary
	if err := apiJSON(http.MethodGet, *controller, "/v1/generative/fabric", nil, &summary); err != nil {
		fmt.Fprintf(os.Stderr, "fabric status request failed: %v\n", err)
		os.Exit(1)
	}

	fmt.Println("NIBIA Fabric Status")
	fmt.Println()
	fmt.Printf("Known / online nodes:       %d / %d\n", summary.KnownNodes, summary.OnlineNodes)
	fmt.Printf("llama.cpp nodes:            %d\n", summary.LlamaCPPInstalledNodes)
	fmt.Printf("RPC coordinators / workers: %d / %d\n", summary.DistributedCoordinatorNodes, summary.DistributedWorkerNodes)
	fmt.Printf("Aggregate node-local RAM:   %.2f GB total / %.2f GB currently available\n",
		float64(summary.AggregateTotalMemoryMB)/1024,
		float64(summary.AggregateAvailableMemoryMB)/1024)
	fmt.Printf("Shared address space:       %t\n", summary.SharedAddressSpace)
	fmt.Printf("Distributed topology ready: %t\n", summary.DistributedGenerativeReady)

	if len(summary.Nodes) == 0 {
		fmt.Println("\nNo llama.cpp-capable nodes discovered.")
		return
	}
	fmt.Println()
	w := tabwriter.NewWriter(os.Stdout, 0, 4, 2, ' ', 0)
	fmt.Fprintln(w, "NODE\tSTATE\tADDRESS\tOS/ARCH\tCPU\tRAM AVAILABLE\tCPU USED\tRAM USED\tCOORD\tWORKER\tLLAMA.CPP")
	for _, node := range summary.Nodes {
		addr := node.PreferredAddress
		if addr == "" {
			addr = "-"
		}
		cpu := node.CPUModel
		if len(cpu) > 28 {
			cpu = cpu[:28]
		}
		fmt.Fprintf(w, "%s\t%s\t%s\t%s/%s\t%s\t%.2f GB\t%.1f%%\t%.1f%%\t%t\t%t\t%s\n",
			node.NodeName, node.NodeState, addr, node.OS, node.Arch, cpu,
			float64(node.MemoryAvailableMB)/1024, node.CPUUsedPct, node.MemoryUsedPct,
			node.CoordinatorCapable, node.WorkerCapable, valueOrDash(node.LlamaCPPVersion))
	}
	_ = w.Flush()
}

func generativeWorkerCmd(args []string) {
	if len(args) < 1 || isHelpArg(args[0]) {
		fabricWorkerUsage()
		return
	}
	action := strings.ToLower(strings.TrimSpace(args[0]))
	if action != "start" && action != "status" && action != "stop" {
		fmt.Fprintf(os.Stderr, "unknown fabric worker action: %s\n\n", args[0])
		fabricWorkerUsage()
		os.Exit(2)
	}

	fs := newCommandFlagSet("fabric worker "+action, "nibia fabric worker "+action+" --node <name-or-id> [options]")
	controller := fs.String("controller", "http://127.0.0.1:8080", "Controller URL")
	node := fs.String("node", "", "target node name or id")
	waitTimeout := fs.Duration("wait-timeout", 30*time.Second, "maximum wait for lifecycle operation")
	port := 50052
	cache := false
	if action == "start" {
		fs.IntVar(&port, "port", 50052, "loopback ggml-rpc-server port")
		fs.BoolVar(&cache, "cache", false, "enable llama.cpp RPC local tensor cache")
	}
	_ = fs.Parse(args[1:])

	if strings.TrimSpace(*node) == "" {
		fmt.Fprintln(os.Stderr, "--node is required")
		os.Exit(2)
	}

	found, err := resolveGenerativeNode(*controller, *node)
	if err != nil {
		fmt.Fprintf(os.Stderr, "fabric worker: %v\n", err)
		os.Exit(1)
	}
	if !found.WorkerCapable {
		fmt.Fprintf(os.Stderr, "node %s does not advertise llama.cpp rpc-worker capability\n", found.NodeName)
		os.Exit(2)
	}

	workloadType := executor.TaskLlamaRPCStatus
	switch action {
	case "start":
		workloadType = executor.TaskLlamaRPCStart
	case "stop":
		workloadType = executor.TaskLlamaRPCStop
	}

	req := types.JobRequest{
		Name: "llamacpp-rpc-" + action + "-" + strings.ToLower(strings.ReplaceAll(found.NodeName, " ", "-")),
		Requirements: types.JobRequirements{
			RequiredNodeID: found.NodeID,
			Capabilities: []string{
				"executor-v1",
				"task-llamacpp-rpc-worker",
				"runtime:llama.cpp",
				"llamacpp:rpc-worker",
			},
			AllowBusy: true,
		},
		Workload: types.WorkloadSpec{
			Type:     workloadType,
			RPCPort:  port,
			RPCCache: cache,
		},
		MaxAttempts: 1,
	}

	var job types.ScheduledJob
	if err := apiJSON(http.MethodPost, *controller, "/v1/jobs", req, &job); err != nil {
		fmt.Fprintf(os.Stderr, "fabric worker %s scheduling failed: %v\n", action, err)
		os.Exit(1)
	}
	job, err = waitScheduledJob(*controller, job, *waitTimeout)
	if err != nil {
		fmt.Fprintf(os.Stderr, "fabric worker %s failed: %v\n", action, err)
		os.Exit(1)
	}
	if !strings.EqualFold(job.State, "SUCCEEDED") || job.Result == nil || !job.Result.Success {
		fmt.Fprintf(os.Stderr, "fabric worker %s ended in %s\n", action, job.State)
		printLeaseAndResult(job)
		os.Exit(1)
	}

	status, ok := decodeLlamaRPCWorkerStatus(job)
	if !ok {
		fmt.Printf("Node:               %s\n", found.NodeName)
		fmt.Printf("Lifecycle action:   %s\n", strings.ToUpper(action))
		printLeaseAndResult(job)
		return
	}
	fmt.Println("NIBIA Managed llama.cpp RPC Worker")
	fmt.Println()
	fmt.Printf("Node:               %s\n", found.NodeName)
	fmt.Printf("Lifecycle action:   %s\n", strings.ToUpper(action))
	fmt.Printf("Managed:            %t\n", status.Managed)
	fmt.Printf("Running:            %t\n", status.Running)
	fmt.Printf("Endpoint:           %s\n", status.Endpoint)
	fmt.Printf("PID:                %d\n", status.PID)
	fmt.Printf("Local tensor cache: %t\n", status.Cache)
	fmt.Printf("Binary:             %s\n", valueOrDash(status.Binary))
	if !status.StartedAt.IsZero() {
		fmt.Printf("Started:            %s\n", status.StartedAt.Local().Format(time.RFC1123))
	}
	if status.ExitError != "" {
		fmt.Printf("Last exit error:    %s\n", status.ExitError)
	}
	if status.Running && !status.TelemetryStartedAt.IsZero() {
		fmt.Printf("Process CPU:        %.1f%%\n", status.ProcessCPUPercent)
		fmt.Printf("Process RSS:        %.2f GiB\n", float64(status.ProcessRSSBytes)/(1024*1024*1024))
		fmt.Printf("Peak process CPU:   %.1f%%\n", status.PeakCPUPercent)
		fmt.Printf("Peak process RSS:   %.2f GiB\n", float64(status.PeakRSSBytes)/(1024*1024*1024))
		fmt.Printf("Telemetry since:    %s\n", status.TelemetryStartedAt.Local().Format(time.RFC1123))
	}
	fmt.Println("Exposure:            loopback only (raw RPC is not exposed on the LAN)")
}

func fabricWorkerUsage() {
	fmt.Println("NIBIA Fabric worker controls")
	fmt.Println()
	fmt.Println("Usage:")
	fmt.Println("  nibia fabric worker start --node <name-or-id> [options]")
	fmt.Println("  nibia fabric worker status --node <name-or-id> [options]")
	fmt.Println("  nibia fabric worker stop --node <name-or-id> [options]")
}

func generativeRelayCmd(args []string) {
	if len(args) < 1 || isHelpArg(args[0]) {
		fabricRelayUsage()
		return
	}
	action := strings.ToLower(strings.TrimSpace(args[0]))
	if action != "start" && action != "status" && action != "probe" && action != "stop" {
		fmt.Fprintf(os.Stderr, "unknown fabric relay action: %s\n\n", args[0])
		fabricRelayUsage()
		os.Exit(2)
	}
	fs := newCommandFlagSet("fabric relay "+action, "nibia fabric relay "+action+" --node <name-or-id> [options]")
	controller := fs.String("controller", "http://127.0.0.1:8080", "Controller URL")
	node := fs.String("node", "", "target node name or id")
	localPort := 55052
	if action == "start" {
		fs.IntVar(&localPort, "local-port", 55052, "controller-loopback relay port")
	}
	_ = fs.Parse(args[1:])
	if strings.TrimSpace(*node) == "" {
		fmt.Fprintln(os.Stderr, "--node is required")
		os.Exit(2)
	}

	found, err := resolveGenerativeNode(*controller, *node)
	if err != nil {
		fmt.Fprintf(os.Stderr, "fabric relay: %v\n", err)
		os.Exit(1)
	}
	path := "/v1/generative/relays/" + found.NodeID

	switch action {
	case "start":
		var status types.GenerativeRelayStatus
		req := types.GenerativeRelayRequest{
			NodeID: found.NodeID, NodeName: found.NodeName,
			LocalPort: localPort, RemotePort: 50052,
		}
		if err := apiJSON(http.MethodPost, *controller, "/v1/generative/relays", req, &status); err != nil {
			fmt.Fprintf(os.Stderr, "fabric relay start failed: %v\n", err)
			os.Exit(1)
		}
		printGenerativeRelay(status, "START")
	case "status":
		var status types.GenerativeRelayStatus
		if err := apiJSON(http.MethodGet, *controller, path, nil, &status); err != nil {
			fmt.Fprintf(os.Stderr, "fabric relay status failed: %v\n", err)
			os.Exit(1)
		}
		status = hydrateGenerativeRelayIdentity(status, found)
		printGenerativeRelay(status, "STATUS")
	case "probe":
		var result types.GenerativeRelayProbeResponse
		if err := apiJSON(http.MethodPost, *controller, path+"/probe", nil, &result); err != nil {
			fmt.Fprintf(os.Stderr, "fabric relay probe failed: %v\n", err)
			os.Exit(1)
		}
		fmt.Println("NIBIA Authenticated Relay Health Probe")
		fmt.Println()
		fmt.Printf("Node:               %s\n", found.NodeName)
		fmt.Printf("Success:            %t\n", result.Success)
		fmt.Printf("Controller endpoint:%s\n", result.LocalEndpoint)
		fmt.Printf("Latency:            %s\n", time.Duration(result.LatencyMS)*time.Millisecond)
		fmt.Println("Probe scope:         authenticated Agent<->Controller tunnel (PING/PONG)")
		fmt.Println("Worker RPC protocol: not touched by this probe")
		fmt.Println("Transport:          mTLS reverse relay")
		fmt.Println("Raw RPC exposure:   none on LAN")
	case "stop":
		var status types.GenerativeRelayStatus
		if err := apiJSON(http.MethodDelete, *controller, path, nil, &status); err != nil {
			fmt.Fprintf(os.Stderr, "fabric relay stop failed: %v\n", err)
			os.Exit(1)
		}
		status = hydrateGenerativeRelayIdentity(status, found)
		printGenerativeRelay(status, "STOP")
	}
}

func fabricRelayUsage() {
	fmt.Println("NIBIA Fabric relay controls")
	fmt.Println()
	fmt.Println("Usage:")
	fmt.Println("  nibia fabric relay start --node <name-or-id> [options]")
	fmt.Println("  nibia fabric relay status --node <name-or-id> [options]")
	fmt.Println("  nibia fabric relay probe --node <name-or-id> [options]")
	fmt.Println("  nibia fabric relay stop --node <name-or-id> [options]")
}

func hydrateGenerativeRelayIdentity(status types.GenerativeRelayStatus, node types.GenerativeNodeCapability) types.GenerativeRelayStatus {
	if strings.TrimSpace(status.NodeID) == "" {
		status.NodeID = node.NodeID
	}
	if strings.TrimSpace(status.NodeName) == "" {
		status.NodeName = node.NodeName
	}
	return status
}

func relayTunnelLabel(status types.GenerativeRelayStatus) string {
	if status.Running {
		return "Ready tunnels"
	}
	return "Agent reverse tunnels"
}

func printGenerativeRelay(status types.GenerativeRelayStatus, action string) {
	fmt.Println("NIBIA Authenticated llama.cpp RPC Relay")
	fmt.Println()
	fmt.Printf("Node:               %s\n", valueOrDash(status.NodeName))
	fmt.Printf("Lifecycle action:   %s\n", action)
	fmt.Printf("Managed:            %t\n", status.Managed)
	fmt.Printf("Running:            %t\n", status.Running)
	fmt.Printf("Controller endpoint:%s\n", valueOrDash(status.LocalEndpoint))
	fmt.Printf("Worker endpoint:    %s\n", valueOrDash(status.RemoteEndpoint))
	fmt.Printf("%-20s%d\n", relayTunnelLabel(status)+":", status.ReadyTunnelCount)
	fmt.Printf("Accepted clients:   %d\n", status.AcceptedConnections)
	fmt.Printf("Successful bridges: %d\n", status.SuccessfulBridges)
	fmt.Printf("Failed bridges:     %d\n", status.FailedBridges)
	fmt.Printf("Active bridges:     %d\n", status.ActiveBridges)
	fmt.Printf("Bytes to worker:    %.2f GiB\n", float64(status.BytesToWorker)/(1024*1024*1024))
	fmt.Printf("Bytes from worker:  %.2f GiB\n", float64(status.BytesFromWorker)/(1024*1024*1024))
	if !status.StartedAt.IsZero() {
		fmt.Printf("Started:            %s\n", status.StartedAt.Local().Format(time.RFC1123))
	}
	if status.LastError != "" {
		fmt.Printf("Last error:         %s\n", status.LastError)
	}
	fmt.Printf("Transport:          %s\n", status.Transport)
	fmt.Printf("Exposure:           %s\n", status.Exposure)
}

func relayEndpointReachable(endpoint string) bool {
	conn, err := net.DialTimeout("tcp", endpoint, time.Second)
	if err != nil {
		return false
	}
	_ = conn.Close()
	return true
}

func resolveGenerativeNode(controller, needle string) (types.GenerativeNodeCapability, error) {
	var summary types.GenerativeFabricSummary
	if err := apiJSON(http.MethodGet, controller, "/v1/generative/fabric", nil, &summary); err != nil {
		return types.GenerativeNodeCapability{}, err
	}
	needle = strings.TrimSpace(needle)
	for _, node := range summary.Nodes {
		if strings.EqualFold(node.NodeID, needle) || strings.EqualFold(node.NodeName, needle) {
			if strings.EqualFold(node.NodeState, "OFFLINE") {
				return types.GenerativeNodeCapability{}, fmt.Errorf("node %s is offline", node.NodeName)
			}
			return node, nil
		}
	}
	return types.GenerativeNodeCapability{}, fmt.Errorf("node %q not found in Fabric", needle)
}

func decodeLlamaRPCWorkerStatus(job types.ScheduledJob) (executor.LlamaRPCWorkerStatus, bool) {
	if job.Result == nil || job.Result.Executor == nil || job.Result.Executor.Data == nil {
		return executor.LlamaRPCWorkerStatus{}, false
	}
	b, err := json.Marshal(job.Result.Executor.Data)
	if err != nil {
		return executor.LlamaRPCWorkerStatus{}, false
	}
	var out executor.LlamaRPCWorkerStatus
	if err := json.Unmarshal(b, &out); err != nil {
		return executor.LlamaRPCWorkerStatus{}, false
	}
	return out, true
}
