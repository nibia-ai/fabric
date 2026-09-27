package executor

import (
	"sync"
	"time"

	"github.com/nibia-ai/fabric/internal/powerguard"
)

type PowerGuardStatus struct {
	Managed    bool      `json:"managed"`
	Active     bool      `json:"active"`
	Backend    string    `json:"backend,omitempty"`
	Reason     string    `json:"reason,omitempty"`
	AcquiredAt time.Time `json:"acquired_at,omitempty"`
	Error      string    `json:"error,omitempty"`
}

var nodePowerGuard = struct {
	sync.Mutex
	guard      *powerguard.Guard
	reason     string
	acquiredAt time.Time
	lastError  string
}{}

func ManagedPowerGuardStatus() PowerGuardStatus {
	nodePowerGuard.Lock()
	defer nodePowerGuard.Unlock()
	return managedPowerGuardStatusLocked()
}

func managedPowerGuardStatusLocked() PowerGuardStatus {
	status := PowerGuardStatus{
		Managed:    true,
		Active:     nodePowerGuard.guard != nil,
		Reason:     nodePowerGuard.reason,
		AcquiredAt: nodePowerGuard.acquiredAt,
		Error:      nodePowerGuard.lastError,
	}
	if nodePowerGuard.guard != nil {
		status.Backend = nodePowerGuard.guard.Backend()
	}
	return status
}

func AcquireManagedPowerGuard(reason string) (PowerGuardStatus, error) {
	nodePowerGuard.Lock()
	defer nodePowerGuard.Unlock()
	if nodePowerGuard.guard != nil {
		return managedPowerGuardStatusLocked(), nil
	}
	guard, err := powerguard.Acquire(reason)
	if err != nil {
		nodePowerGuard.lastError = err.Error()
		return managedPowerGuardStatusLocked(), err
	}
	nodePowerGuard.guard = guard
	nodePowerGuard.reason = reason
	nodePowerGuard.acquiredAt = time.Now().UTC()
	nodePowerGuard.lastError = ""
	return managedPowerGuardStatusLocked(), nil
}

func ReleaseManagedPowerGuard() (PowerGuardStatus, error) {
	nodePowerGuard.Lock()
	guard := nodePowerGuard.guard
	nodePowerGuard.guard = nil
	nodePowerGuard.reason = ""
	nodePowerGuard.acquiredAt = time.Time{}
	nodePowerGuard.Unlock()
	if guard != nil {
		if err := guard.Release(); err != nil {
			nodePowerGuard.Lock()
			nodePowerGuard.lastError = err.Error()
			status := managedPowerGuardStatusLocked()
			nodePowerGuard.Unlock()
			return status, err
		}
	}
	nodePowerGuard.Lock()
	nodePowerGuard.lastError = ""
	status := managedPowerGuardStatusLocked()
	nodePowerGuard.Unlock()
	return status, nil
}
