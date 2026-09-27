package executor

import (
	"errors"
	"net"
	"os/exec"
	"testing"
)

func TestNormalizeRPCPort(t *testing.T) {
	port, err := normalizeRPCPort(0)
	if err != nil || port != defaultLlamaRPCPort {
		t.Fatalf("default port=%d err=%v", port, err)
	}
	if _, err := normalizeRPCPort(80); err == nil {
		t.Fatal("expected privileged/low port to be rejected")
	}
	if _, err := normalizeRPCPort(70000); err == nil {
		t.Fatal("expected out-of-range port to be rejected")
	}
}

func TestStartManagedLlamaRPCRefusesUnmanagedListener(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	port := ln.Addr().(*net.TCPAddr).Port
	if port < minLlamaRPCPort {
		t.Skip("ephemeral port unexpectedly below lifecycle minimum")
	}
	if _, err := StartManagedLlamaRPC(port, false); err == nil {
		t.Fatal("expected unmanaged listener protection")
	}
}

func TestManagedLlamaRPCStatusDefault(t *testing.T) {
	_ = StopManagedLlamaRPC()
	status := ManagedLlamaRPCStatus()
	if status.Running {
		t.Fatalf("unexpected running state: %+v", status)
	}
	if status.Endpoint != "127.0.0.1:50052" {
		t.Fatalf("endpoint=%q", status.Endpoint)
	}
}

func TestManagedLlamaRPCIntentionalStopDoesNotReportExitError(t *testing.T) {
	cmd := &exec.Cmd{}
	llamaRPCManager.Lock()
	prev := llamaRPCManager.state
	llamaRPCManager.state = managedLlamaRPC{
		cmd:           cmd,
		running:       true,
		stopRequested: true,
		exitError:     "stale error",
	}
	llamaRPCManager.Unlock()
	defer func() {
		llamaRPCManager.Lock()
		llamaRPCManager.state = prev
		llamaRPCManager.Unlock()
	}()

	recordManagedLlamaRPCExit(cmd, errors.New("signal: killed"))
	status := ManagedLlamaRPCStatus()
	if status.Running {
		t.Fatalf("intentional stop should leave worker stopped: %+v", status)
	}
	if status.ExitError != "" {
		t.Fatalf("intentional stop should not report exit error: %q", status.ExitError)
	}
}

func TestManagedLlamaRPCUnexpectedExitStillReportsError(t *testing.T) {
	cmd := &exec.Cmd{}
	llamaRPCManager.Lock()
	prev := llamaRPCManager.state
	llamaRPCManager.state = managedLlamaRPC{cmd: cmd, running: true}
	llamaRPCManager.Unlock()
	defer func() {
		llamaRPCManager.Lock()
		llamaRPCManager.state = prev
		llamaRPCManager.Unlock()
	}()

	recordManagedLlamaRPCExit(cmd, errors.New("unexpected exit"))
	status := ManagedLlamaRPCStatus()
	if status.Running {
		t.Fatalf("unexpected exit should leave worker stopped: %+v", status)
	}
	if status.ExitError != "unexpected exit" {
		t.Fatalf("unexpected exit should remain visible, got %q", status.ExitError)
	}
}

func TestManagedLlamaRPCUnexpectedCleanExitIsStillVisible(t *testing.T) {
	cmd := &exec.Cmd{}
	llamaRPCManager.Lock()
	prev := llamaRPCManager.state
	llamaRPCManager.state = managedLlamaRPC{cmd: cmd, running: true}
	llamaRPCManager.Unlock()
	defer func() {
		llamaRPCManager.Lock()
		llamaRPCManager.state = prev
		llamaRPCManager.Unlock()
	}()

	recordManagedLlamaRPCExit(cmd, nil)
	status := ManagedLlamaRPCStatus()
	if status.ExitError != "process exited unexpectedly" {
		t.Fatalf("unexpected clean exit should remain visible, got %q", status.ExitError)
	}
}
