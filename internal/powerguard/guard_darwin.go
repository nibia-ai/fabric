//go:build darwin

package powerguard

import (
	"fmt"
	"io"
	"os/exec"
	"strings"
	"time"
)

// acquirePlatform holds a real macOS idle-system-sleep assertion for the
// lifetime of the Guard. This release deliberately does not rely on `caffeinate
// -w <parent-pid>`: physical acceptance showed that NIBIA could report the
// helper as started while the worker later entered Idle Sleep. Instead NIBIA
// owns a long-lived `caffeinate -i` child and verifies the assertion through
// pmset before reporting the guard ACTIVE.
func acquirePlatform(reason string) (func() error, string, error) {
	path, err := exec.LookPath("caffeinate")
	if err != nil {
		return nil, "", fmt.Errorf("macOS caffeinate not found: %w", err)
	}
	cmd := exec.Command(path, "-i")
	cmd.Stdout = io.Discard
	cmd.Stderr = io.Discard
	if err := cmd.Start(); err != nil {
		return nil, "", fmt.Errorf("start macOS power assertion: %w", err)
	}

	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()

	if err := verifyDarwinAssertion(cmd.Process.Pid, done); err != nil {
		if cmd.Process != nil {
			_ = cmd.Process.Kill()
		}
		select {
		case <-done:
		case <-time.After(time.Second):
		}
		return nil, "", err
	}

	return func() error {
		if cmd.Process != nil {
			_ = cmd.Process.Kill()
		}
		select {
		case <-done:
			// A killed caffeinate process normally returns a signal error. The
			// assertion has still been released successfully, so cleanup remains
			// idempotent and quiet.
			return nil
		case <-time.After(2 * time.Second):
			return fmt.Errorf("timeout releasing macOS caffeinate assertion")
		}
	}, "macOS/caffeinate verified", nil
}

func verifyDarwinAssertion(pid int, done <-chan error) error {
	deadline := time.Now().Add(2 * time.Second)
	needle := fmt.Sprintf("pid %d(caffeinate)", pid)
	for time.Now().Before(deadline) {
		select {
		case err := <-done:
			if err == nil {
				return fmt.Errorf("macOS caffeinate exited before power assertion became active")
			}
			return fmt.Errorf("macOS caffeinate exited before power assertion became active: %v", err)
		default:
		}

		out, err := exec.Command("/usr/bin/pmset", "-g", "assertions").CombinedOutput()
		if err == nil {
			for _, line := range strings.Split(string(out), "\n") {
				if strings.Contains(line, needle) && strings.Contains(line, "PreventUserIdleSystemSleep") {
					return nil
				}
			}
		}
		time.Sleep(100 * time.Millisecond)
	}
	return fmt.Errorf("macOS caffeinate started but no PreventUserIdleSystemSleep assertion was verified")
}
