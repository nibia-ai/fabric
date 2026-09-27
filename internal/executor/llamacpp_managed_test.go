package executor

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/nibia-ai/fabric/internal/llamaruntime"
)

func TestCollectLlamaCPPInventoryUsesVerifiedManagedMetadata(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	if runtime.GOOS == "windows" {
		t.Setenv("USERPROFILE", home)
	}
	t.Setenv("PATH", "")

	root := llamaruntime.DefaultRoot()
	binDir := filepath.Join(root, "bin")
	if err := os.MkdirAll(binDir, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"llama-cli", "llama-server", "ggml-rpc-server"} {
		if runtime.GOOS == "windows" {
			name += ".exe"
		}
		// Deliberately not a runnable binary: managed inventory must trust the
		// already-verified install metadata instead of re-executing probes.
		if err := os.WriteFile(filepath.Join(binDir, name), []byte("managed-runtime-test"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	meta := map[string]any{
		"tag":          llamaruntime.Tag,
		"commit":       llamaruntime.CommitFull,
		"asset":        "test",
		"sha256":       "test",
		"bin_dir":      "bin",
		"version":      "version: 0.4.0-dev (build 10902, commit df03399b8)",
		"installed_at": "2026-09-13T00:00:00Z",
	}
	b, err := json.Marshal(meta)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "runtime.json"), b, 0o644); err != nil {
		t.Fatal(err)
	}

	inv, ok := collectLlamaCPPInventory(context.Background())
	if !ok {
		t.Fatal("managed llama.cpp inventory not discovered")
	}
	if !strings.Contains(inv.Version, "df03399b8") {
		t.Fatalf("version=%q want managed metadata identity", inv.Version)
	}
	if !containsFold(inv.Features, "rpc-coordinator") || !containsFold(inv.Features, "rpc-worker") {
		t.Fatalf("features=%v want coordinator+worker", inv.Features)
	}
}
