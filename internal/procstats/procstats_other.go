//go:build !linux && !darwin && !windows

package procstats

import (
	"fmt"
)

func readSnapshot(pid int) (Snapshot, error) {
	return Snapshot{}, fmt.Errorf("process telemetry unsupported on this platform for pid %d", pid)
}
