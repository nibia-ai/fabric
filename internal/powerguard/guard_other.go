//go:build !darwin && !linux && !windows

package powerguard

import (
	"fmt"
	"runtime"
)

func acquirePlatform(reason string) (func() error, string, error) {
	return nil, "", fmt.Errorf("power guard is not implemented for %s", runtime.GOOS)
}
