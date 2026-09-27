package agentlock

import (
	"os"
	"path/filepath"
	"testing"
)

func TestAcquireRejectsSecondInstance(t *testing.T) {
	dir := t.TempDir()
	first, err := Acquire(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer first.Release()

	if _, err := Acquire(dir); err == nil {
		t.Fatal("expected second Agent lock acquisition to fail")
	}
}

func TestReleaseAllowsRestart(t *testing.T) {
	dir := t.TempDir()
	first, err := Acquire(dir)
	if err != nil {
		t.Fatal(err)
	}
	if err := first.Release(); err != nil {
		t.Fatal(err)
	}
	second, err := Acquire(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer second.Release()
}

func TestMalformedLockIsRecovered(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "agent.lock")
	if err := os.WriteFile(path, []byte("not-json"), 0o600); err != nil {
		t.Fatal(err)
	}
	lock, err := Acquire(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer lock.Release()
}
