package agentlock

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

type record struct {
	PID       int       `json:"pid"`
	Hostname  string    `json:"hostname"`
	StartedAt time.Time `json:"started_at"`
}

type Lock struct {
	path string
}

func Acquire(stateDir string) (*Lock, error) {
	if stateDir == "" {
		return nil, errors.New("state directory is required")
	}
	if err := os.MkdirAll(stateDir, 0o700); err != nil {
		return nil, err
	}
	path := filepath.Join(stateDir, "agent.lock")

	for attempt := 0; attempt < 2; attempt++ {
		f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
		if err == nil {
			host, _ := os.Hostname()
			rec := record{PID: os.Getpid(), Hostname: host, StartedAt: time.Now().UTC()}
			encErr := json.NewEncoder(f).Encode(rec)
			closeErr := f.Close()
			if encErr != nil {
				_ = os.Remove(path)
				return nil, encErr
			}
			if closeErr != nil {
				_ = os.Remove(path)
				return nil, closeErr
			}
			return &Lock{path: path}, nil
		}
		if !errors.Is(err, os.ErrExist) {
			return nil, err
		}

		existing, readErr := readRecord(path)
		if readErr == nil && existing.PID > 0 && processAlive(existing.PID) {
			return nil, fmt.Errorf(
				"another NIBIA Agent is already active for this state directory (pid=%d host=%s started=%s)",
				existing.PID, existing.Hostname, existing.StartedAt.Local().Format(time.RFC3339),
			)
		}

		// Stale lock from an unclean exit. Remove once and retry atomically.
		if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
			return nil, fmt.Errorf("remove stale agent lock: %w", err)
		}
	}
	return nil, errors.New("unable to acquire NIBIA Agent state lock")
}

func readRecord(path string) (record, error) {
	var rec record
	f, err := os.Open(path)
	if err != nil {
		return rec, err
	}
	defer f.Close()
	err = json.NewDecoder(f).Decode(&rec)
	return rec, err
}

func (l *Lock) Release() error {
	if l == nil || l.path == "" {
		return nil
	}
	err := os.Remove(l.path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	return err
}
