//go:build linux

package powerguard

import (
	"errors"
	"fmt"
	"io"
	"os/exec"
	"syscall"
	"time"
)

func acquirePlatform(reason string) (func() error, string, error) {
	path, err := exec.LookPath("systemd-inhibit")
	if err != nil {
		return nil, "", fmt.Errorf("systemd-inhibit not found: %w", err)
	}
	if reason == "" {
		reason = "active NIBIA workload"
	}
	cmd := exec.Command(path,
		"--what=sleep:idle",
		"--who=NIBIA",
		"--why="+reason,
		"--mode=block",
		"/bin/sh", "-c", "while :; do sleep 3600; done",
	)
	// systemd-inhibit owns a child command for the lifetime of the inhibitor.
	// Keep the helper and its child in their own process group so release can
	// terminate the complete tree instead of waiting indefinitely on a wrapper
	// whose child survived a parent-only kill.
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Stdout = io.Discard
	cmd.Stderr = io.Discard
	if err := cmd.Start(); err != nil {
		return nil, "", fmt.Errorf("start Linux sleep inhibitor: %w", err)
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	return func() error {
		return stopLinuxProcessGroup(cmd, done)
	}, "linux/systemd-inhibit", nil
}

func stopLinuxProcessGroup(cmd *exec.Cmd, done <-chan error) error {
	if cmd == nil || cmd.Process == nil {
		return nil
	}
	pid := cmd.Process.Pid

	// SIGTERM first so systemd-inhibit and its child can exit normally.
	if err := syscall.Kill(-pid, syscall.SIGTERM); err != nil && !errors.Is(err, syscall.ESRCH) {
		return fmt.Errorf("terminate Linux systemd-inhibit process group: %w", err)
	}
	select {
	case <-done:
		return nil
	case <-time.After(2 * time.Second):
	}

	// Cleanup must be bounded: a stuck power-guard release must never pin an
	// Agent job until the Controller cancels it. Escalate to SIGKILL and wait
	// once more for the owned process tree to be reaped.
	if err := syscall.Kill(-pid, syscall.SIGKILL); err != nil && !errors.Is(err, syscall.ESRCH) {
		return fmt.Errorf("kill Linux systemd-inhibit process group: %w", err)
	}
	select {
	case <-done:
		return nil
	case <-time.After(2 * time.Second):
		return fmt.Errorf("timeout releasing Linux systemd-inhibit process group")
	}
}
