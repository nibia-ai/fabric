package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/nibia-ai/fabric/internal/discovery"
	"github.com/nibia-ai/fabric/internal/identity"
	"github.com/nibia-ai/fabric/internal/types"
	"github.com/nibia-ai/fabric/internal/version"
)

const (
	headerVersion  = "X-Nibia-Version"
	headerProtocol = "X-Nibia-Protocol"
)

func main() {
	if len(os.Args) < 2 {
		usage()
		return
	}
	switch os.Args[1] {
	case "help", "--help", "-h":
		if len(os.Args) > 2 && strings.EqualFold(os.Args[2], "advanced") {
			advancedUsage()
			return
		}
		usage()
	case "version":
		fmt.Printf("NIBIA v%s\nProtocol: %s\nPlatform: %s/%s\n", version.Version, version.ProtocolVersion, runtime.GOOS, runtime.GOARCH)
	case "setup":
		setupCmd(os.Args[2:])
	case "doctor":
		doctorCmd(os.Args[2:])
	case "discover":
		discoverCmd(os.Args[2:])
	case "pair":
		pairCmd(os.Args[2:])
	case "trust":
		trustCmd(os.Args[2:])
	case "agent":
		agentCmd(os.Args[2:])
	case "runtime":
		runtimeCmd(os.Args[2:])
	case "run":
		generativeRunCmd(os.Args[2:])
	case "serve":
		generativeServeCmd(os.Args[2:])
	case "fabric":
		fabricCmd(os.Args[2:])
	case "nodes":
		nodesCmd(os.Args[2:])
	case "node":
		nodeCmd(os.Args[2:])
	case "controller":
		controllerCmd(os.Args[2:])
	default:
		fmt.Fprintf(os.Stderr, "unknown command: %s\n\n", os.Args[1])
		usage()
		os.Exit(2)
	}
}

func usage() {
	fmt.Println("NIBIA Fabric")
	fmt.Println()
	fmt.Println("Usage:")
	fmt.Println("  nibia <command> [options]")
	fmt.Println()
	fmt.Println("Core:")
	fmt.Println("  serve       Keep a GGUF model loaded and expose the local Web UI/API")
	fmt.Println("  run         Run one-shot or interactive terminal inference")
	fmt.Println("  nodes       Show Fabric nodes, capacity, and runtime parity")
	fmt.Println("  doctor      Check local or Fabric health")
	fmt.Println("  discover    Discover NIBIA Controllers on the LAN")
	fmt.Println("  pair        Create a short-lived node pairing code")
	fmt.Println()
	fmt.Println("Security:")
	fmt.Println("  trust list")
	fmt.Println("  trust revoke <node>")
	fmt.Println("  node inspect <node>")
	fmt.Println()
	fmt.Println("Other:")
	fmt.Println("  version")
	fmt.Println("  help advanced")
	fmt.Println()
	fmt.Println("Use 'nibia <command> -h' or 'nibia <command> --help' for command-specific options.")
}

func advancedUsage() {
	fmt.Println("NIBIA Fabric advanced / maintenance commands")
	fmt.Println()
	fmt.Println("  setup")
	fmt.Println("  runtime status|ensure")
	fmt.Println("  agent reset --state-dir <path> --yes")
	fmt.Println("  controller info")
	fmt.Println("  fabric status")
	fmt.Println("  fabric devices [--node <name-or-id> | --nodes <name-or-id,...>]")
	fmt.Println("  fabric worker start|status|stop --node <name-or-id>")
	fmt.Println("  fabric relay start|status|probe|stop --node <name-or-id>")
}

func discoverCmd(args []string) {
	fs := newCommandFlagSet("discover", "nibia discover [options]")
	timeout := fs.Duration("timeout", 3*time.Second, "discovery timeout")
	includeVirtual := fs.Bool("include-virtual", false, "include VPN/tunnel/virtual interfaces")
	noLocal := fs.Bool("no-local", false, "disable localhost controller fallback")
	localPort := fs.Int("local-port", 8080, "localhost pairing/info port used by fallback")
	verbose := fs.Bool("verbose", false, "show interfaces used for discovery")
	_ = fs.Parse(args)

	ifaces, ifaceErr := discovery.Interfaces(*includeVirtual)
	if *verbose {
		if ifaceErr != nil {
			fmt.Printf("Interface enumeration failed: %v\n", ifaceErr)
		} else if len(ifaces) == 0 {
			fmt.Println("Discovery interfaces: none")
		} else {
			fmt.Println("Discovery interfaces:")
			for _, iface := range ifaces {
				kind := "physical"
				if iface.Virtual {
					kind = "virtual"
				}
				fmt.Printf("  %-10s %-15s private=%t preferred=%t %s\n",
					iface.Name, iface.Address, iface.Private, iface.Preferred, kind)
			}
		}
		fmt.Println()
	}

	ctx, cancel := context.WithTimeout(context.Background(), *timeout+time.Second)
	defer cancel()

	var networkItems []discovery.Message
	var networkErr error
	if ifaceErr == nil && len(ifaces) > 0 {
		networkItems, networkErr = discovery.Discover(
			ctx,
			*timeout,
			version.ProtocolVersion,
			discovery.Options{IncludeVirtual: *includeVirtual},
		)
	}

	var localItems []discovery.Message
	if !*noLocal {
		localURL := fmt.Sprintf("http://127.0.0.1:%d", *localPort)
		if item, err := localDiscovery(localURL); err == nil {
			localItems = append(localItems, item)
		} else if *verbose {
			fmt.Printf("Local fallback: %v\n\n", err)
		}
	}

	items := discovery.MergeMessages(networkItems, localItems)
	if len(items) == 0 {
		fmt.Println("No NIBIA fabrics found.")
		if networkErr != nil {
			fmt.Printf("Network discovery warning: %v\n", networkErr)
		}
		return
	}

	for i, item := range items {
		if i > 0 {
			fmt.Println()
		}
		compat := "compatible"
		if item.Protocol != version.ProtocolVersion {
			compat = "INCOMPATIBLE"
		}
		fmt.Printf("Found NIBIA Fabric\n")
		fmt.Printf("  Host:        %s\n", item.Hostname)
		fmt.Printf("  Pair URL:    %s\n", item.PairURL)
		fmt.Printf("  Secure URL:  %s\n", item.SecureURL)
		fmt.Printf("  Version:     %s\n", item.Version)
		fmt.Printf("  Protocol:    %s (%s)\n", item.Protocol, compat)
		fmt.Printf("  Fingerprint: %s\n", item.Fingerprint)
		fmt.Printf("  Discovery:   %s\n", item.Discovery)
	}
}

func localDiscovery(controller string) (discovery.Message, error) {
	client := &http.Client{Timeout: 700 * time.Millisecond}
	resp, err := client.Get(strings.TrimRight(controller, "/") + "/v1/info")
	if err != nil {
		return discovery.Message{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return discovery.Message{}, fmt.Errorf("localhost controller returned %s", resp.Status)
	}

	var info types.ControllerInfo
	if err := json.NewDecoder(resp.Body).Decode(&info); err != nil {
		return discovery.Message{}, err
	}
	if info.Fingerprint == "" {
		return discovery.Message{}, fmt.Errorf("localhost endpoint is not a secure NIBIA controller")
	}

	hostname, _ := os.Hostname()
	pairURL := controller
	if info.SecureControllerURL != "" {
		if u, err := url.Parse(info.SecureControllerURL); err == nil {
			if h := u.Hostname(); h != "" {
				port := 8080
				if cu, err := url.Parse(controller); err == nil {
					if p := cu.Port(); p != "" {
						if parsed, err := strconv.Atoi(p); err == nil {
							port = parsed
						}
					}
				}
				pairURL = "http://" + net.JoinHostPort(h, strconv.Itoa(port))
			}
		}
	}

	return discovery.Message{
		Type:        discovery.ResponseType,
		Name:        info.Name,
		Hostname:    hostname,
		PairURL:     pairURL,
		SecureURL:   info.SecureControllerURL,
		Version:     info.Version,
		Protocol:    info.ProtocolVersion,
		Fingerprint: info.Fingerprint,
		Discovery:   "localhost-fallback",
	}, nil
}

func pairCmd(args []string) {
	if len(args) == 1 && strings.EqualFold(strings.TrimSpace(args[0]), "help") {
		pairUsage()
		return
	}
	fs := newCommandFlagSet("pair", "nibia pair [options]")
	controller := fs.String("controller", "http://127.0.0.1:8080", "Controller pairing URL")
	_ = fs.Parse(args)
	if fs.NArg() != 0 {
		fmt.Fprintf(os.Stderr, "unexpected pair argument: %s\n\n", fs.Arg(0))
		pairUsage()
		os.Exit(2)
	}

	var out types.PairCodeResponse
	if err := apiJSON(http.MethodPost, *controller, "/v1/pairing/code", nil, &out); err != nil {
		fmt.Fprintf(os.Stderr, "create pairing code failed: %v\n", err)
		os.Exit(1)
	}
	fmt.Printf("Pairing code: %s\n", out.Code)
	fmt.Printf("Expires:      %s\n", out.ExpiresAt.Local().Format(time.RFC1123))
	fmt.Println()
	fmt.Println("On the device you want to add:")
	fmt.Printf("  nibia-agent --pair <PAIR-URL> --code %s --name <NODE-NAME>\n", out.Code)
}

func pairUsage() {
	fmt.Println("NIBIA Fabric pairing")
	fmt.Println()
	fmt.Println("Usage:")
	fmt.Println("  nibia pair [options]")
	fmt.Println()
	fmt.Println("Options:")
	fmt.Println("  --controller string")
	fmt.Println("      Controller pairing URL (default \"http://127.0.0.1:8080\")")
}

func trustCmd(args []string) {
	if len(args) < 1 || isHelpArg(args[0]) {
		trustUsage()
		return
	}
	switch args[0] {
	case "list":
		fs := newCommandFlagSet("trust list", "nibia trust list [options]")
		controller := fs.String("controller", "http://127.0.0.1:8080", "Controller URL")
		_ = fs.Parse(args[1:])
		var records []types.TrustRecord
		if err := apiJSON(http.MethodGet, *controller, "/v1/trust", nil, &records); err != nil {
			fmt.Fprintf(os.Stderr, "trust list failed: %v\n", err)
			os.Exit(1)
		}
		w := tabwriter.NewWriter(os.Stdout, 0, 4, 2, ' ', 0)
		fmt.Fprintln(w, "NAME\tNODE ID\tSTATUS\tISSUED")
		for _, r := range records {
			status := "TRUSTED"
			if r.RevokedAt != nil {
				status = "REVOKED"
			}
			fmt.Fprintf(w, "%s\t%s\t%s\t%s\n", r.Name, r.NodeID, status, r.IssuedAt.Local().Format("2006-01-02 15:04"))
		}
		_ = w.Flush()
	case "revoke":
		if len(args) >= 2 && isHelpArg(args[1]) {
			trustRevokeUsage()
			return
		}
		if len(args) < 2 {
			fmt.Fprintln(os.Stderr, "trust revoke requires a node name or id")
			trustRevokeUsage()
			os.Exit(2)
		}
		needle := args[1]
		fs := newCommandFlagSet("trust revoke", "nibia trust revoke <node-name-or-id> [options]")
		controller := fs.String("controller", "http://127.0.0.1:8080", "Controller URL")
		_ = fs.Parse(args[2:])
		var record types.TrustRecord
		if err := apiJSON(http.MethodPost, *controller, "/v1/trust/revoke", types.RevokeRequest{Node: needle}, &record); err != nil {
			fmt.Fprintf(os.Stderr, "revoke failed: %v\n", err)
			os.Exit(1)
		}
		fmt.Printf("Revoked trust for %s (%s)\n", record.Name, record.NodeID)
	default:
		fmt.Fprintf(os.Stderr, "unknown trust action: %s\n\n", args[0])
		trustUsage()
		os.Exit(2)
	}
}

func trustUsage() {
	fmt.Println("NIBIA Fabric trust management")
	fmt.Println()
	fmt.Println("Usage:")
	fmt.Println("  nibia trust list [options]")
	fmt.Println("  nibia trust revoke <node-name-or-id> [options]")
}

func trustRevokeUsage() {
	fmt.Println("NIBIA Fabric")
	fmt.Println()
	fmt.Println("Usage:")
	fmt.Println("  nibia trust revoke <node-name-or-id> [options]")
	fmt.Println()
	fmt.Println("Options:")
	fmt.Println("  --controller string")
	fmt.Println("      Controller URL (default \"http://127.0.0.1:8080\")")
}

func agentCmd(args []string) {
	if len(args) < 1 || isHelpArg(args[0]) {
		agentUsage()
		return
	}
	if args[0] != "reset" {
		fmt.Fprintf(os.Stderr, "unknown agent action: %s\n\n", args[0])
		agentUsage()
		os.Exit(2)
	}

	fs := newCommandFlagSet("agent reset", "nibia agent reset [options]")
	stateDir := fs.String("state-dir", identity.DefaultAgentDir(), "agent identity/state directory")
	yes := fs.Bool("yes", false, "confirm destructive identity reset")
	_ = fs.Parse(args[1:])

	if !*yes {
		fmt.Fprintln(os.Stderr, "refusing to reset identity without --yes")
		fmt.Fprintf(os.Stderr, "target state directory: %s\n", *stateDir)
		os.Exit(2)
	}

	removed, err := identity.ResetAgentIdentity(*stateDir)
	if err != nil {
		fmt.Fprintf(os.Stderr, "identity reset failed: %v\n", err)
		os.Exit(1)
	}
	if len(removed) == 0 {
		fmt.Printf("No NIBIA agent credentials found in %s\n", *stateDir)
		return
	}
	fmt.Printf("Reset NIBIA agent identity in %s\n", *stateDir)
	for _, path := range removed {
		fmt.Printf("  removed %s\n", path)
	}
	fmt.Println("The device must be paired again before it can send secure heartbeats.")
}

func agentUsage() {
	fmt.Println("NIBIA Fabric Agent maintenance")
	fmt.Println()
	fmt.Println("Usage:")
	fmt.Println("  nibia agent reset [options]")
}

func printLeaseAndResult(job types.ScheduledJob) {
	if len(job.Attempts) > 0 {
		fmt.Println()
		fmt.Println("Attempts:")
		w := tabwriter.NewWriter(os.Stdout, 0, 4, 2, ' ', 0)
		fmt.Fprintln(w, "  #\tNODE\tSTATUS\tFAILURE\tSUMMARY")
		for _, a := range job.Attempts {
			fmt.Fprintf(w, "  %d\t%s\t%s\t%s\t%s\n",
				a.Attempt,
				valueOrDash(a.NodeName),
				valueOrDash(a.Status),
				valueOrDash(a.FailureKind),
				valueOrDash(a.Summary),
			)
		}
		_ = w.Flush()
	}
	if job.ActiveLease != nil {
		fmt.Println()
		fmt.Println("Lease:")
		fmt.Printf("  ID:              %s\n", job.ActiveLease.LeaseID)
		fmt.Printf("  Node:            %s\n", job.ActiveLease.NodeName)
		fmt.Printf("  Status:          %s\n", job.ActiveLease.Status)
		fmt.Printf("  Attempt:         %d\n", job.ActiveLease.Attempt)
		fmt.Printf("  Expires:         %s\n", job.ActiveLease.ExpiresAt.Local().Format(time.RFC1123))
		fmt.Printf("  Cancel requested:%t\n", job.ActiveLease.CancelRequested)
	}
	if job.Result != nil {
		fmt.Println()
		fmt.Println("Result:")
		fmt.Printf("  Node:            %s\n", job.Result.NodeName)
		fmt.Printf("  Success:         %t\n", job.Result.Success)
		fmt.Printf("  Duration:        %d ms\n", job.Result.DurationMS)
		fmt.Printf("  Summary:         %s\n", job.Result.Summary)
		if job.Result.Executor != nil {
			fmt.Printf("  Executor kind:   %s\n", job.Result.Executor.Kind)
			if b, err := json.MarshalIndent(job.Result.Executor.Data, "    ", "  "); err == nil {
				fmt.Printf("  Data:\n    %s\n", string(b))
			}
		}
	}
}

func valueOrDash(v string) string {
	if strings.TrimSpace(v) == "" {
		return "-"
	}
	return v
}

func terminalJobState(state string) bool {
	switch strings.ToUpper(strings.TrimSpace(state)) {
	case "SUCCEEDED", "FAILED", "CANCELLED":
		return true
	default:
		return false
	}
}

func cancelScheduledJobBestEffort(controller, jobID string) {
	jobID = strings.TrimSpace(jobID)
	if jobID == "" {
		return
	}
	var ignored types.CancelJobResponse
	_ = apiJSON(http.MethodPost, controller, "/v1/jobs/cancel/"+jobID, nil, &ignored)
}

func waitScheduledJob(controller string, job types.ScheduledJob, timeout time.Duration) (types.ScheduledJob, error) {
	deadline := time.Now().Add(timeout)
	for !terminalJobState(job.State) {
		if strings.EqualFold(job.State, "UNSCHEDULABLE") {
			return job, fmt.Errorf("job %s is unschedulable", job.ID)
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

func nodesCmd(args []string) {
	fs := newCommandFlagSet("nodes", "nibia nodes [options]")
	controller := fs.String("controller", "http://127.0.0.1:8080", "Controller URL")
	_ = fs.Parse(args)

	var nodes []types.NodeStatus
	if err := apiJSON(http.MethodGet, *controller, "/v1/nodes", nil, &nodes); err != nil {
		fmt.Fprintf(os.Stderr, "request failed: %v\n", err)
		os.Exit(1)
	}
	sort.Slice(nodes, func(i, j int) bool { return nodes[i].Node.Name < nodes[j].Node.Name })

	if info, err := fetchControllerInfo(*controller); err == nil {
		state := controllerCompatibilityLabel(info)
		fmt.Printf("Controller: v%s (%s)\n", info.Version, state)
	}

	runtimeSummary, runtimeErr := getGenerativeFabricSummary(*controller)
	runtimeByID := map[string]types.GenerativeNodeCapability{}
	runtimeByName := map[string]types.GenerativeNodeCapability{}
	localRuntime := ""
	if runtimeErr == nil {
		for _, rn := range runtimeSummary.Nodes {
			runtimeByID[strings.ToLower(strings.TrimSpace(rn.NodeID))] = rn
			runtimeByName[strings.ToLower(strings.TrimSpace(rn.NodeName))] = rn
		}
		if local, ok := localGenerativeNode(runtimeSummary); ok {
			localRuntime = strings.TrimSpace(local.LlamaCPPVersion)
		}
	}
	if localRuntime != "" {
		fmt.Printf("Runtime: llama.cpp %s\n", llamaRuntimeShortLabel(localRuntime))
	} else {
		fmt.Println("Runtime: llama.cpp unknown")
	}

	w := tabwriter.NewWriter(os.Stdout, 0, 4, 2, ' ', 0)
	fmt.Fprintln(w, "NAME\tSTATE\tADDRESS\tOS/ARCH\tPLATFORM\tCORES P/L\tCPU USED\tRAM AVAILABLE\tSAFE USABLE\tMEM HEADROOM\tPRESSURE\tSCORE\tAGENT\tRUNTIME\tLAST SEEN")
	var runtimeMismatches []string
	for _, n := range nodes {
		cpuUsed := "n/a"
		if n.Node.CPUUsedPct >= 0 {
			cpuUsed = fmt.Sprintf("%.1f%%", n.Node.CPUUsedPct)
		}
		headroom := "n/a"
		pressure := strings.ToUpper(strings.TrimSpace(n.Node.MemoryPressureLevel))
		if pressure != "" && n.Node.MemoryHeadroomPct >= 0 {
			headroom = fmt.Sprintf("%.0f%%", n.Node.MemoryHeadroomPct)
		}
		if pressure == "" {
			pressure = "n/a"
		}
		platform := n.Node.CPUModel
		if platform == "" {
			platform = n.Node.OS + "/" + n.Node.Arch
		}
		if len(platform) > 28 {
			platform = platform[:28]
		}
		address := n.Node.PreferredAddress
		if address == "" {
			address = "-"
		}
		runtimeState := "UNKNOWN"
		var rn types.GenerativeNodeCapability
		var foundRuntime bool
		if v, ok := runtimeByID[strings.ToLower(strings.TrimSpace(n.Node.ID))]; ok {
			rn, foundRuntime = v, true
		} else if v, ok := runtimeByName[strings.ToLower(strings.TrimSpace(n.Node.Name))]; ok {
			rn, foundRuntime = v, true
		}
		if foundRuntime && strings.TrimSpace(rn.LlamaCPPVersion) != "" && localRuntime != "" {
			if llamaRuntimeCompatible(localRuntime, rn.LlamaCPPVersion) {
				runtimeState = "MATCH"
			} else {
				runtimeState = "MISMATCH"
				runtimeMismatches = append(runtimeMismatches, fmt.Sprintf("%s=%s", n.Node.Name, llamaRuntimeShortLabel(rn.LlamaCPPVersion)))
			}
		}
		reserveMiB := adaptiveMemoryReserveMiB(n.Node.MemoryTotalMB, n.Node.MemoryAvailableMB)
		safeUsableMiB := subtractReserve(n.Node.MemoryAvailableMB, reserveMiB)
		fmt.Fprintf(w, "%s\t%s\t%s\t%s/%s\t%s\t%dP/%dL\t%s\t%.1f/%.1f GB\t%.1f GB\t%s\t%s\t%.2f\tv%s\t%s\t%s\n",
			n.Node.Name, effectiveState(n), address, n.Node.OS, n.Node.Arch, platform, n.Node.CPUPhysical, n.Node.CPULogical,
			cpuUsed, float64(n.Node.MemoryAvailableMB)/1024, float64(n.Node.MemoryTotalMB)/1024, float64(safeUsableMiB)/1024,
			headroom, pressure, n.Node.ResourceScore, n.Node.AgentVersion, runtimeState, time.Since(n.LastSeen).Round(time.Second))
	}
	_ = w.Flush()
	fmt.Println("Safe usable: default SAFE memory policy (auto 10% physical RAM reserve; min 512 MiB, max 4 GiB).")
	for _, mismatch := range runtimeMismatches {
		fmt.Printf("⚠ Runtime mismatch: %s (Primary=%s)\n", mismatch, llamaRuntimeShortLabel(localRuntime))
	}
}

func llamaRuntimeShortLabel(raw string) string {
	id := parseLlamaRuntimeIdentity(raw)
	if id.Commit != "" {
		if len(id.Commit) > 7 {
			return id.Commit[:7]
		}
		return id.Commit
	}
	if id.Version != "" {
		return id.Version
	}
	return "unknown"
}

func nodeCmd(args []string) {
	if len(args) < 1 || isHelpArg(args[0]) {
		nodeUsage()
		return
	}
	if args[0] != "inspect" {
		fmt.Fprintf(os.Stderr, "unknown node action: %s\n\n", args[0])
		nodeUsage()
		os.Exit(2)
	}
	controller := "http://127.0.0.1:8080"
	needle := ""
	rest := args[1:]
	for i := 0; i < len(rest); i++ {
		if isHelpArg(rest[i]) {
			nodeInspectUsage()
			return
		}
		switch {
		case rest[i] == "--controller" && i+1 < len(rest):
			controller = rest[i+1]
			i++
		case strings.HasPrefix(rest[i], "--controller="):
			controller = strings.TrimPrefix(rest[i], "--controller=")
		case strings.HasPrefix(rest[i], "-"):
			fmt.Fprintf(os.Stderr, "unknown flag: %s\n", rest[i])
			os.Exit(2)
		case needle == "":
			needle = rest[i]
		}
	}
	if needle == "" {
		fmt.Fprintln(os.Stderr, "node name or id is required")
		os.Exit(2)
	}
	var nodes []types.NodeStatus
	if err := apiJSON(http.MethodGet, controller, "/v1/nodes", nil, &nodes); err != nil {
		fmt.Fprintf(os.Stderr, "request failed: %v\n", err)
		os.Exit(1)
	}
	for _, found := range nodes {
		if strings.EqualFold(found.Node.Name, needle) || strings.EqualFold(found.Node.ID, needle) {
			printNode(found)
			return
		}
	}
	fmt.Fprintf(os.Stderr, "node %q not found\n", needle)
	os.Exit(1)
}

func nodeUsage() {
	fmt.Println("NIBIA Fabric node inspection")
	fmt.Println()
	fmt.Println("Usage:")
	fmt.Println("  nibia node inspect <name-or-id> [options]")
}

func nodeInspectUsage() {
	fmt.Println("NIBIA Fabric")
	fmt.Println()
	fmt.Println("Usage:")
	fmt.Println("  nibia node inspect <name-or-id> [options]")
	fmt.Println()
	fmt.Println("Options:")
	fmt.Println("  --controller string")
	fmt.Println("      Controller URL (default \"http://127.0.0.1:8080\")")
}

func controllerCmd(args []string) {
	if len(args) < 1 || isHelpArg(args[0]) {
		controllerUsage()
		return
	}
	if args[0] != "info" {
		fmt.Fprintf(os.Stderr, "unknown controller action: %s\n\n", args[0])
		controllerUsage()
		os.Exit(2)
	}
	fs := newCommandFlagSet("controller info", "nibia controller info [options]")
	controller := fs.String("controller", "http://127.0.0.1:8080", "controller URL")
	_ = fs.Parse(args[1:])

	client := &http.Client{Timeout: 5 * time.Second}
	resp, err := client.Get(strings.TrimRight(*controller, "/") + "/v1/info")
	if err != nil {
		fmt.Fprintf(os.Stderr, "request failed: %v\n", err)
		os.Exit(1)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		fmt.Fprintf(os.Stderr, "controller returned %s\n", resp.Status)
		os.Exit(1)
	}
	var info types.ControllerInfo
	if err := json.NewDecoder(resp.Body).Decode(&info); err != nil {
		fmt.Fprintf(os.Stderr, "invalid response: %v\n", err)
		os.Exit(1)
	}
	compatible := "yes"
	if info.ProtocolVersion != version.ProtocolVersion {
		compatible = "no"
	}
	fmt.Printf("Name:        %s\n", info.Name)
	fmt.Printf("Version:     %s\n", info.Version)
	fmt.Printf("Protocol:    %s\n", info.ProtocolVersion)
	fmt.Printf("Compatible:  %s (CLI protocol %s)\n", compatible, version.ProtocolVersion)
	fmt.Printf("Secure URL:  %s\n", info.SecureControllerURL)
	fmt.Printf("Fingerprint: %s\n", info.Fingerprint)
	fmt.Printf("Discovery:   %s\n", info.Discovery)
}

func controllerUsage() {
	fmt.Println("NIBIA Fabric Controller inspection")
	fmt.Println()
	fmt.Println("Usage:")
	fmt.Println("  nibia controller info [options]")
}

func apiJSON(method, controller, path string, input, output any) error {
	var body io.Reader
	if input != nil {
		b, err := json.Marshal(input)
		if err != nil {
			return err
		}
		body = bytes.NewReader(b)
	}
	req, err := http.NewRequest(method, strings.TrimRight(controller, "/")+path, body)
	if err != nil {
		return err
	}
	req.Header.Set(headerVersion, version.Version)
	req.Header.Set(headerProtocol, version.ProtocolVersion)
	if input != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := (&http.Client{Timeout: 8 * time.Second}).Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return fmt.Errorf("%s: %s", resp.Status, strings.TrimSpace(string(b)))
	}
	if output != nil {
		return json.NewDecoder(resp.Body).Decode(output)
	}
	return nil
}

func effectiveState(n types.NodeStatus) string {
	if strings.EqualFold(n.State, "offline") {
		return "OFFLINE"
	}
	if n.Node.ResourceState == "" {
		return "ONLINE"
	}
	return strings.ToUpper(n.Node.ResourceState)
}

func printNode(found types.NodeStatus) {
	n := found.Node
	fmt.Printf("Name:              %s\n", n.Name)
	fmt.Printf("ID:                %s\n", n.ID)
	fmt.Printf("State:             %s\n", effectiveState(found))
	fmt.Printf("Agent version:     %s\n", n.AgentVersion)
	fmt.Printf("Protocol version:  %s\n", n.ProtocolVersion)
	fmt.Printf("Platform:          %s (%s/%s)\n", n.Platform, n.OS, n.Arch)
	if n.PreferredAddress != "" {
		fmt.Printf("Preferred address: %s\n", n.PreferredAddress)
	}
	if len(n.NetworkInterfaces) > 0 {
		fmt.Println("Network interfaces:")
		for _, iface := range n.NetworkInterfaces {
			tags := []string{}
			if iface.Preferred {
				tags = append(tags, "preferred")
			}
			if iface.Virtual {
				tags = append(tags, "virtual/VPN")
			}
			if iface.Private {
				tags = append(tags, "private")
			}
			fmt.Printf("  %-16s %-15s %s\n", iface.Name, iface.Address, strings.Join(tags, ","))
		}
	}
	fmt.Printf("CPU model:         %s\n", n.CPUModel)
	fmt.Printf("CPU cores:         %d physical / %d logical\n", n.CPUPhysical, n.CPULogical)
	fmt.Printf("CPU used:          %.1f%%\n", n.CPUUsedPct)
	fmt.Printf("Load avg:          %.2f / %.2f / %.2f\n", n.Load1, n.Load5, n.Load15)
	fmt.Printf("RAM:               %.2f GB available / %.2f GB total (%.1f%% used)\n",
		float64(n.MemoryAvailableMB)/1024, float64(n.MemoryTotalMB)/1024, n.MemoryUsedPct)
	if n.MemoryPressurePct >= 0 {
		fmt.Printf("Memory pressure:   %.0f%% free\n", n.MemoryPressurePct)
	} else {
		fmt.Printf("Memory pressure:   n/a\n")
	}
	fmt.Printf("Compressed memory: %.2f GB\n", float64(n.CompressedMB)/1024)
	fmt.Printf("Swap:              %.2f GB used / %.2f GB total\n", float64(n.SwapUsedMB)/1024, float64(n.SwapTotalMB)/1024)
	fmt.Printf("Uptime:            %s\n", (time.Duration(n.UptimeSeconds) * time.Second).Round(time.Minute))
	fmt.Printf("Resource score:    %.2f\n", n.ResourceScore)
	fmt.Printf("Capabilities:      %s\n", strings.Join(n.Capabilities, ", "))
	if len(n.Runtimes) > 0 {
		fmt.Println("Runtimes:")
		for _, rt := range n.Runtimes {
			fmt.Printf("  %-12s installed=%t reachable=%t version=%s features=%s\n", rt.Name, rt.Installed, rt.Reachable, valueOrDash(rt.Version), valueOrDash(strings.Join(rt.Features, ",")))
		}
	}
	fmt.Printf("Last seen:         %s ago\n", time.Since(found.LastSeen).Round(time.Second))
}
