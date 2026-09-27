package executor

import (
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"

	"github.com/nibia-ai/fabric/internal/llamaruntime"
	"strconv"
	"sync"
	"time"

	"github.com/nibia-ai/fabric/internal/procstats"
)

const (
	defaultLlamaRPCPort = 50052
	minLlamaRPCPort     = 1024
	maxLlamaRPCPort     = 65535
)

// LlamaRPCWorkerStatus describes the single loopback-only ggml-rpc-server
// process managed by one NIBIA Agent. Raw llama.cpp RPC stays private to the
// node; cross-node traffic is carried through NIBIA's authenticated reverse relay.
type LlamaRPCWorkerStatus struct {
	Managed            bool      `json:"managed"`
	Running            bool      `json:"running"`
	PID                int       `json:"pid,omitempty"`
	Endpoint           string    `json:"endpoint"`
	Port               int       `json:"port"`
	Cache              bool      `json:"cache"`
	Binary             string    `json:"binary,omitempty"`
	StartedAt          time.Time `json:"started_at,omitempty"`
	ExitError          string    `json:"exit_error,omitempty"`
	ProcessCPUPercent  float64   `json:"process_cpu_pct,omitempty"`
	ProcessRSSBytes    uint64    `json:"process_rss_bytes,omitempty"`
	PeakCPUPercent     float64   `json:"peak_cpu_pct,omitempty"`
	PeakRSSBytes       uint64    `json:"peak_rss_bytes,omitempty"`
	TelemetryStartedAt time.Time `json:"telemetry_started_at,omitempty"`
}

type managedLlamaRPC struct {
	cmd                *exec.Cmd
	done               chan struct{}
	port               int
	cache              bool
	binary             string
	startedAt          time.Time
	running            bool
	exitError          string
	processCPUPercent  float64
	processRSSBytes    uint64
	peakCPUPercent     float64
	peakRSSBytes       uint64
	telemetryStartedAt time.Time
	telemetryResetSeq  uint64
	stopRequested      bool
}

var llamaRPCManager = struct {
	sync.Mutex
	state managedLlamaRPC
}{}

func normalizeRPCPort(port int) (int, error) {
	if port == 0 {
		port = defaultLlamaRPCPort
	}
	if port < minLlamaRPCPort || port > maxLlamaRPCPort {
		return 0, fmt.Errorf("RPC port must be between %d and %d", minLlamaRPCPort, maxLlamaRPCPort)
	}
	return port, nil
}

func managedLlamaRPCStatusLocked() LlamaRPCWorkerStatus {
	s := llamaRPCManager.state
	out := LlamaRPCWorkerStatus{
		Managed:            s.cmd != nil,
		Running:            s.running,
		Port:               s.port,
		Cache:              s.cache,
		Binary:             s.binary,
		StartedAt:          s.startedAt,
		ExitError:          s.exitError,
		ProcessCPUPercent:  s.processCPUPercent,
		ProcessRSSBytes:    s.processRSSBytes,
		PeakCPUPercent:     s.peakCPUPercent,
		PeakRSSBytes:       s.peakRSSBytes,
		TelemetryStartedAt: s.telemetryStartedAt,
	}
	if out.Port == 0 {
		out.Port = defaultLlamaRPCPort
	}
	out.Endpoint = net.JoinHostPort("127.0.0.1", strconv.Itoa(out.Port))
	if s.cmd != nil && s.cmd.Process != nil && s.running {
		out.PID = s.cmd.Process.Pid
	}
	return out
}

func ManagedLlamaRPCStatus() LlamaRPCWorkerStatus {
	llamaRPCManager.Lock()
	defer llamaRPCManager.Unlock()
	return managedLlamaRPCStatusLocked()
}

// ResetManagedLlamaRPCTelemetry starts a fresh process-scoped measurement
// window without restarting the worker. This lets one inference report only
// the CPU/RSS peaks attributable to its own execution window.
func ResetManagedLlamaRPCTelemetry() LlamaRPCWorkerStatus {
	llamaRPCManager.Lock()
	defer llamaRPCManager.Unlock()
	llamaRPCManager.state.processCPUPercent = 0
	llamaRPCManager.state.processRSSBytes = 0
	llamaRPCManager.state.peakCPUPercent = 0
	llamaRPCManager.state.peakRSSBytes = 0
	llamaRPCManager.state.telemetryStartedAt = time.Now().UTC()
	llamaRPCManager.state.telemetryResetSeq++
	return managedLlamaRPCStatusLocked()
}

func findLlamaRPCBinary() (string, error) {
	for _, name := range []string{"ggml-rpc-server", "llama-rpc-server"} {
		if path, _, err := llamaruntime.ResolveBinary(name); err == nil {
			return path, nil
		}
	}
	return "", errors.New("llama.cpp RPC worker binary not available")
}

func loopbackPortOpen(port int) bool {
	address := net.JoinHostPort("127.0.0.1", strconv.Itoa(port))
	conn, err := net.DialTimeout("tcp", address, 150*time.Millisecond)
	if err != nil {
		return false
	}
	_ = conn.Close()
	return true
}

func StartManagedLlamaRPC(port int, cache bool) (LlamaRPCWorkerStatus, error) {
	port, err := normalizeRPCPort(port)
	if err != nil {
		return LlamaRPCWorkerStatus{}, err
	}

	llamaRPCManager.Lock()
	if llamaRPCManager.state.running {
		status := managedLlamaRPCStatusLocked()
		llamaRPCManager.Unlock()
		if status.Port != port || status.Cache != cache {
			return status, fmt.Errorf("managed RPC worker is already running on %s", status.Endpoint)
		}
		return status, nil
	}
	// Do not hijack or kill an arbitrary listener that NIBIA did not start.
	if loopbackPortOpen(port) {
		llamaRPCManager.Unlock()
		return LlamaRPCWorkerStatus{}, fmt.Errorf("loopback port %d is already in use by an unmanaged process", port)
	}
	binary, err := findLlamaRPCBinary()
	if err != nil {
		llamaRPCManager.Unlock()
		return LlamaRPCWorkerStatus{}, err
	}

	args := []string{"--host", "127.0.0.1", "--port", strconv.Itoa(port)}
	if cache {
		args = append(args, "-c")
	}
	cmd := exec.Command(binary, args...)
	cmd.Stdout = io.Discard
	cmd.Stderr = io.Discard
	if err := cmd.Start(); err != nil {
		llamaRPCManager.Unlock()
		return LlamaRPCWorkerStatus{}, fmt.Errorf("start llama.cpp RPC worker: %w", err)
	}

	done := make(chan struct{})
	llamaRPCManager.state = managedLlamaRPC{
		cmd:                cmd,
		done:               done,
		port:               port,
		cache:              cache,
		binary:             binary,
		startedAt:          time.Now().UTC(),
		running:            true,
		telemetryStartedAt: time.Now().UTC(),
		telemetryResetSeq:  1,
	}
	llamaRPCManager.Unlock()

	go monitorManagedLlamaRPCProcess(cmd, done)

	go func(c *exec.Cmd, done chan struct{}) {
		err := c.Wait()
		recordManagedLlamaRPCExit(c, err)
		close(done)
	}(cmd, done)

	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		status := ManagedLlamaRPCStatus()
		if !status.Running {
			if status.ExitError == "" {
				status.ExitError = "process exited before loopback endpoint became reachable"
			}
			return status, errors.New(status.ExitError)
		}
		if loopbackPortOpen(port) {
			return status, nil
		}
		time.Sleep(100 * time.Millisecond)
	}

	status := ManagedLlamaRPCStatus()
	_ = StopManagedLlamaRPC()
	return status, fmt.Errorf("RPC worker did not become reachable on %s", status.Endpoint)
}

func recordManagedLlamaRPCExit(cmd *exec.Cmd, err error) {
	llamaRPCManager.Lock()
	defer llamaRPCManager.Unlock()
	if llamaRPCManager.state.cmd != cmd {
		return
	}
	llamaRPCManager.state.running = false
	if llamaRPCManager.state.stopRequested {
		// Process.Kill is the expected cross-platform teardown primitive for the
		// managed loopback RPC worker. Its Wait result is non-zero on Linux and
		// Windows, but a requested teardown is not a worker failure.
		llamaRPCManager.state.exitError = ""
	} else if err != nil {
		llamaRPCManager.state.exitError = err.Error()
	} else {
		llamaRPCManager.state.exitError = "process exited unexpectedly"
	}
	llamaRPCManager.state.stopRequested = false
}

func monitorManagedLlamaRPCProcess(cmd *exec.Cmd, done <-chan struct{}) {
	if cmd == nil || cmd.Process == nil {
		return
	}
	tracker := procstats.NewTracker(cmd.Process.Pid)
	var seenReset uint64
	sample := func() {
		llamaRPCManager.Lock()
		if llamaRPCManager.state.cmd != cmd {
			llamaRPCManager.Unlock()
			return
		}
		seq := llamaRPCManager.state.telemetryResetSeq
		llamaRPCManager.Unlock()
		if seq != seenReset {
			tracker.Reset()
			seenReset = seq
		}
		m, err := tracker.Sample()
		if err != nil {
			return
		}
		_, peakCPU, peakRSS := tracker.Values()
		llamaRPCManager.Lock()
		if llamaRPCManager.state.cmd == cmd && llamaRPCManager.state.telemetryResetSeq == seq {
			llamaRPCManager.state.processCPUPercent = m.CPUPercent
			llamaRPCManager.state.processRSSBytes = m.RSSBytes
			llamaRPCManager.state.peakCPUPercent = peakCPU
			llamaRPCManager.state.peakRSSBytes = peakRSS
		}
		llamaRPCManager.Unlock()
	}
	sample()
	tick := time.NewTicker(time.Second)
	defer tick.Stop()
	for {
		select {
		case <-done:
			sample()
			return
		case <-tick.C:
			sample()
		}
	}
}

func StopManagedLlamaRPC() error {
	llamaRPCManager.Lock()
	state := llamaRPCManager.state
	if state.cmd == nil || !state.running || state.cmd.Process == nil {
		llamaRPCManager.state.running = false
		llamaRPCManager.Unlock()
		return nil
	}
	proc := state.cmd.Process
	done := state.done
	llamaRPCManager.state.stopRequested = true
	llamaRPCManager.Unlock()

	if err := proc.Kill(); err != nil && !errors.Is(err, os.ErrProcessDone) {
		llamaRPCManager.Lock()
		if llamaRPCManager.state.cmd == state.cmd {
			llamaRPCManager.state.stopRequested = false
		}
		llamaRPCManager.Unlock()
		return fmt.Errorf("stop llama.cpp RPC worker: %w", err)
	}
	select {
	case <-done:
		return nil
	case <-time.After(2 * time.Second):
		llamaRPCManager.Lock()
		if llamaRPCManager.state.cmd == state.cmd {
			llamaRPCManager.state.stopRequested = false
		}
		llamaRPCManager.Unlock()
		return errors.New("timed out waiting for llama.cpp RPC worker to stop")
	}
}

// ShutdownManagedRuntimes is called by the Agent on graceful shutdown so raw
// runtime helpers do not survive after the NIBIA trust/process boundary exits.
func ShutdownManagedRuntimes() {
	_ = StopManagedLlamaRPC()
	_, _ = ReleaseManagedPowerGuard()
}
