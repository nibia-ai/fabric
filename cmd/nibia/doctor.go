package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"runtime"
	"sort"
	"strings"
	"time"

	"github.com/nibia-ai/fabric/internal/llamaruntime"
	"github.com/nibia-ai/fabric/internal/powerguard"
	"github.com/nibia-ai/fabric/internal/types"
	"github.com/nibia-ai/fabric/internal/version"
)

func fetchControllerInfo(controller string) (types.ControllerInfo, error) {
	client := &http.Client{Timeout: 5 * time.Second}
	resp, err := client.Get(strings.TrimRight(controller, "/") + "/v1/info")
	if err != nil {
		return types.ControllerInfo{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return types.ControllerInfo{}, fmt.Errorf("controller returned %s", resp.Status)
	}
	var info types.ControllerInfo
	if err := json.NewDecoder(resp.Body).Decode(&info); err != nil {
		return types.ControllerInfo{}, err
	}
	return info, nil
}

func controllerCompatibilityLabel(info types.ControllerInfo) string {
	if strings.TrimSpace(info.ProtocolVersion) != version.ProtocolVersion {
		return "PROTOCOL MISMATCH"
	}
	if strings.TrimSpace(info.Version) != version.Version {
		return "VERSION MISMATCH"
	}
	return "MATCH"
}

func setupCmd(args []string) {
	fs := newCommandFlagSet("setup", "nibia setup [options]")
	timeout := fs.Duration("timeout", 20*time.Minute, "maximum runtime bootstrap time")
	_ = fs.Parse(args)
	if *timeout <= 0 {
		fmt.Fprintln(os.Stderr, "--timeout must be positive")
		os.Exit(2)
	}

	fmt.Printf("NIBIA setup — v%s (%s/%s)\n", version.Version, runtime.GOOS, runtime.GOARCH)
	st := llamaruntime.StatusCurrent()
	if st.Installed {
		fmt.Printf("✓ Managed runtime ready: llama.cpp %s (%s)\n", llamaruntime.Tag, llamaruntime.Commit)
		fmt.Printf("  %s\n", st.BinDir)
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), *timeout)
	defer cancel()
	st, err := llamaruntime.Ensure(ctx, os.Stdout)
	if err != nil {
		fmt.Fprintf(os.Stderr, "✗ Managed runtime setup failed: %v\n", err)
		os.Exit(1)
	}
	fmt.Printf("✓ Managed runtime ready: llama.cpp %s (%s)\n", llamaruntime.Tag, llamaruntime.Commit)
	fmt.Printf("  %s\n", st.BinDir)
}

func doctorCmd(args []string) {
	fs := newCommandFlagSet("doctor", "nibia doctor [options]")
	localOnly := fs.Bool("local", false, "check only this machine; skip Controller/fabric checks")
	controller := fs.String("controller", "http://127.0.0.1:8080", "controller URL")
	_ = fs.Parse(args)

	fmt.Println("NIBIA Doctor")
	fmt.Printf("Version:  v%s\n", version.Version)
	fmt.Printf("Platform: %s/%s\n\n", runtime.GOOS, runtime.GOARCH)

	failed := false
	fmt.Println("[1/4] Managed runtime")
	st := llamaruntime.StatusCurrent()
	if st.Installed {
		fmt.Printf("  ✓ llama.cpp %s (%s) READY\n", llamaruntime.Tag, llamaruntime.Commit)
		if st.Version != "" {
			fmt.Printf("    %s\n", st.Version)
		}
	} else {
		fmt.Printf("  ✗ managed llama.cpp %s (%s) is not installed\n", llamaruntime.Tag, llamaruntime.Commit)
		fmt.Println("    Run: nibia setup")
		failed = true
	}

	fmt.Println("\n[2/4] Power guard")
	guard, guardErr := powerguard.Acquire("NIBIA doctor preflight")
	if guardErr != nil {
		fmt.Printf("  ✗ unavailable: %v\n", guardErr)
		failed = true
	} else {
		fmt.Printf("  ✓ supported: %s\n", guard.Backend())
		if err := guard.Release(); err != nil {
			fmt.Printf("  ✗ release failed: %v\n", err)
			failed = true
		}
	}

	if *localOnly {
		fmt.Println("\n[3/4] Controller")
		fmt.Println("  - skipped (--local)")
		fmt.Println("\n[4/4] Fabric")
		fmt.Println("  - skipped (--local)")
		if failed {
			fmt.Println("\nResult: FAIL")
			os.Exit(1)
		}
		fmt.Println("\nResult: PASS (local)")
		return
	}

	fmt.Println("\n[3/4] Controller")
	info, err := fetchControllerInfo(*controller)
	if err != nil {
		fmt.Printf("  ✗ unreachable: %v\n", err)
		failed = true
	} else {
		label := controllerCompatibilityLabel(info)
		if label == "MATCH" {
			fmt.Printf("  ✓ v%s, protocol %s — MATCH\n", info.Version, info.ProtocolVersion)
		} else {
			fmt.Printf("  ✗ v%s, protocol %s — %s (CLI v%s / protocol %s)\n",
				info.Version, info.ProtocolVersion, label, version.Version, version.ProtocolVersion)
			failed = true
		}
	}

	fmt.Println("\n[4/4] Fabric")
	var nodes []types.NodeStatus
	if err := apiJSON(http.MethodGet, *controller, "/v1/nodes", nil, &nodes); err != nil {
		fmt.Printf("  ✗ nodes unavailable: %v\n", err)
		failed = true
	} else {
		sort.Slice(nodes, func(i, j int) bool { return nodes[i].Node.Name < nodes[j].Node.Name })
		ready := 0
		agentMatch := 0
		for _, n := range nodes {
			if effectiveState(n) == "READY" {
				ready++
			}
			if strings.TrimSpace(n.Node.AgentVersion) == version.Version {
				agentMatch++
			}
		}
		if len(nodes) > 0 && ready == len(nodes) {
			fmt.Printf("  ✓ nodes READY: %d/%d\n", ready, len(nodes))
		} else {
			fmt.Printf("  ✗ nodes READY: %d/%d\n", ready, len(nodes))
			failed = true
		}
		if len(nodes) > 0 && agentMatch == len(nodes) {
			fmt.Printf("  ✓ Agent versions: %d/%d MATCH\n", agentMatch, len(nodes))
		} else {
			fmt.Printf("  ✗ Agent versions: %d/%d MATCH CLI v%s\n", agentMatch, len(nodes), version.Version)
			failed = true
		}

		summary, err := getGenerativeFabricSummary(*controller)
		if err != nil {
			fmt.Printf("  ✗ runtime parity unavailable: %v\n", err)
			failed = true
		} else {
			runtimeByID := map[string]types.GenerativeNodeCapability{}
			runtimeByName := map[string]types.GenerativeNodeCapability{}
			for _, rn := range summary.Nodes {
				runtimeByID[strings.ToLower(strings.TrimSpace(rn.NodeID))] = rn
				runtimeByName[strings.ToLower(strings.TrimSpace(rn.NodeName))] = rn
			}
			runtimeMatch := 0
			for _, n := range nodes {
				var rn types.GenerativeNodeCapability
				var ok bool
				if rn, ok = runtimeByID[strings.ToLower(strings.TrimSpace(n.Node.ID))]; !ok {
					rn, ok = runtimeByName[strings.ToLower(strings.TrimSpace(n.Node.Name))]
				}
				if ok && strings.Contains(strings.ToLower(rn.LlamaCPPVersion), strings.ToLower(llamaruntime.Commit)) {
					runtimeMatch++
				}
			}
			if len(nodes) > 0 && runtimeMatch == len(nodes) {
				fmt.Printf("  ✓ Runtime parity: %d/%d MATCH (%s)\n", runtimeMatch, len(nodes), llamaruntime.Commit)
			} else {
				fmt.Printf("  ✗ Runtime parity: %d/%d MATCH (%s)\n", runtimeMatch, len(nodes), llamaruntime.Commit)
				failed = true
			}
		}
	}

	if failed {
		fmt.Println("\nResult: FAIL")
		os.Exit(1)
	}
	fmt.Println("\nResult: PASS")
}
