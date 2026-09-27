package main

import (
	"context"
	"fmt"
	"os"
	"time"

	"github.com/nibia-ai/fabric/internal/llamaruntime"
)

func resolveLlamaBinaryAuto(ctx context.Context, name string) (string, error) {
	p, _, err := llamaruntime.ResolveOrEnsure(ctx, name, os.Stderr)
	if err != nil {
		return "", err
	}
	return p, nil
}

func runtimeCmd(args []string) {
	if len(args) < 1 || isHelpArg(args[0]) {
		runtimeUsage()
		return
	}
	switch args[0] {
	case "status":
		runtimeStatusCmd(args[1:])
	case "ensure":
		runtimeEnsureCmd(args[1:])
	default:
		fmt.Fprintf(os.Stderr, "unknown runtime command: %s\n", args[0])
		runtimeUsage()
		os.Exit(2)
	}
}

func runtimeUsage() {
	fmt.Println("NIBIA Fabric managed runtime")
	fmt.Println()
	fmt.Println("Usage:")
	fmt.Println("  nibia runtime status")
	fmt.Println("  nibia runtime ensure [options]")
}

func runtimeStatusCmd(args []string) {
	fs := newCommandFlagSet("runtime status", "nibia runtime status")
	_ = fs.Parse(args)

	st := llamaruntime.StatusCurrent()
	fmt.Printf("Runtime:  llama.cpp %s (%s)\n", llamaruntime.Tag, llamaruntime.Commit)
	fmt.Printf("Platform: %s/%s\n", st.Artifact.OS, st.Artifact.Arch)
	if st.Installed {
		fmt.Println("Status:   READY (NIBIA-managed)")
		fmt.Printf("Path:     %s\n", st.BinDir)
		if st.Version != "" {
			fmt.Printf("Identity: %s\n", st.Version)
		}
		return
	}

	// Keep development fallback visible without making it the product path.
	if p, _, err := llamaruntime.ResolveBinary("llama-cli"); err == nil {
		fmt.Println("Status:   EXTERNAL runtime available (development fallback)")
		fmt.Printf("Path:     %s\n", p)
		fmt.Println("Managed:  not installed")
		return
	}
	fmt.Println("Status:   NOT INSTALLED")
	fmt.Printf("Target:   %s\n", st.Root)
}

func runtimeEnsureCmd(args []string) {
	fs := newCommandFlagSet("runtime ensure", "nibia runtime ensure [options]")
	timeout := fs.Duration("timeout", 20*time.Minute, "maximum runtime download/install time")
	_ = fs.Parse(args)
	if *timeout <= 0 {
		fmt.Fprintln(os.Stderr, "--timeout must be positive")
		os.Exit(2)
	}

	ctx, cancel := context.WithTimeout(context.Background(), *timeout)
	defer cancel()
	st, err := llamaruntime.Ensure(ctx, os.Stdout)
	if err != nil {
		fmt.Fprintf(os.Stderr, "runtime ensure: %v\n", err)
		os.Exit(1)
	}
	fmt.Printf("Managed runtime: llama.cpp %s (%s)\n", llamaruntime.Tag, llamaruntime.Commit)
	fmt.Printf("Path:            %s\n", st.BinDir)
}
