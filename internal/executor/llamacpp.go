package executor

import (
	"context"
	"os/exec"
	"strings"
	"time"

	"github.com/nibia-ai/fabric/internal/llamaruntime"
	"github.com/nibia-ai/fabric/internal/types"
)

// collectLlamaCPPInventory discovers only fixed, known llama.cpp binaries.
// It never executes a user-supplied path or argument. The resulting feature
// inventory is used to advertise coordinator and RPC-worker capability.
func collectLlamaCPPInventory(parent context.Context) (types.RuntimeInventory, bool) {
	managed := llamaruntime.StatusCurrent()
	bins := map[string]string{}
	for _, name := range []string{"llama-cli", "llama-server", "ggml-rpc-server", "llama-rpc-server"} {
		if path, _, err := llamaruntime.ResolveBinary(name); err == nil {
			bins[name] = path
		}
	}
	if len(bins) == 0 {
		return types.RuntimeInventory{}, false
	}

	features := make([]string, 0, 4)
	if _, ok := bins["llama-cli"]; ok {
		features = append(features, "cli")
	}
	if _, ok := bins["llama-server"]; ok {
		features = append(features, "server")
	}
	if _, ok := bins["ggml-rpc-server"]; ok {
		features = append(features, "rpc-worker")
	} else if _, ok := bins["llama-rpc-server"]; ok {
		features = append(features, "rpc-worker")
	}

	// A NIBIA-managed runtime has already passed pinned-artifact SHA-256 and
	// executable identity verification during installation. Re-running --help
	// and --version on every Agent inventory refresh is both redundant and slow
	// (and can exhaust the Agent's telemetry timeout). Trust the verified managed
	// metadata here; retain active probing only for external/development runtimes.
	coordinator := false
	if managed.Installed {
		_, hasCLI := bins["llama-cli"]
		_, hasServer := bins["llama-server"]
		coordinator = hasCLI || hasServer
	} else {
		for _, name := range []string{"llama-cli", "llama-server"} {
			path, ok := bins[name]
			if !ok {
				continue
			}
			if llamaCPPHasRPCFlag(parent, path) {
				coordinator = true
				break
			}
		}
	}
	if coordinator {
		features = append(features, "rpc-coordinator")
	}

	version := ""
	if managed.Installed {
		version = strings.TrimSpace(managed.Version)
	}
	if version == "" {
		for _, name := range []string{"llama-cli", "llama-server", "ggml-rpc-server", "llama-rpc-server"} {
			path, ok := bins[name]
			if !ok {
				continue
			}
			if v := llamaCPPVersion(parent, path); v != "" {
				version = v
				break
			}
		}
	}

	if status := ManagedLlamaRPCStatus(); status.Running {
		features = append(features, "rpc-worker-managed-loopback")
	}
	return types.RuntimeInventory{
		Name:      "llama.cpp",
		Version:   version,
		Installed: true,
		Reachable: true,
		Features:  uniqueStrings(features),
	}, true
}

func llamaCPPHasRPCFlag(parent context.Context, path string) bool {
	ctx, cancel := context.WithTimeout(parent, 2*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, path, "--help").CombinedOutput()
	if err != nil && len(out) == 0 {
		return false
	}
	text := strings.ToLower(string(out))
	return strings.Contains(text, "--rpc")
}

func llamaCPPVersion(parent context.Context, path string) string {
	ctx, cancel := context.WithTimeout(parent, 2*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, path, "--version").CombinedOutput()
	if err != nil && len(out) == 0 {
		return ""
	}
	for _, line := range strings.Split(string(out), "\n") {
		line = strings.TrimSpace(line)
		if line != "" {
			if len(line) > 120 {
				line = line[:120]
			}
			return line
		}
	}
	return ""
}

func containsFold(values []string, target string) bool {
	for _, value := range values {
		if strings.EqualFold(strings.TrimSpace(value), strings.TrimSpace(target)) {
			return true
		}
	}
	return false
}
