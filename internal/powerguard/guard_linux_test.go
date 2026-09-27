//go:build linux

package powerguard

import (
	"os/exec"
	"syscall"
	"testing"
	"time"
)

func TestStopLinuxProcessGroupStopsNormalGroup(t *testing.T) {
	cmd := exec.Command("/bin/sh", "-c", "while :; do sleep 60; done")
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	started := time.Now()
	if err := stopLinuxProcessGroup(cmd, done); err != nil {
		t.Fatalf("stopLinuxProcessGroup: %v", err)
	}
	if d := time.Since(started); d > 3*time.Second {
		t.Fatalf("normal process-group cleanup took too long: %s", d)
	}
}

func TestStopLinuxProcessGroupEscalatesWhenTermIgnored(t *testing.T) {
	cmd := exec.Command("/bin/sh", "-c", "trap '' TERM; while :; do sleep 60; done")
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	// Let the shell install its TERM trap before exercising escalation.
	time.Sleep(100 * time.Millisecond)
	started := time.Now()
	if err := stopLinuxProcessGroup(cmd, done); err != nil {
		t.Fatalf("stopLinuxProcessGroup: %v", err)
	}
	d := time.Since(started)
	if d < 1500*time.Millisecond {
		t.Fatalf("expected SIGKILL escalation path, cleanup finished too quickly: %s", d)
	}
	if d > 5*time.Second {
		t.Fatalf("escalated process-group cleanup took too long: %s", d)
	}
}
