//go:build windows

package powerguard

import (
	"fmt"
	"runtime"
	"syscall"
)

const (
	esSystemRequired = 0x00000001
	esContinuous     = 0x80000000
)

func acquirePlatform(reason string) (func() error, string, error) {
	ready := make(chan error, 1)
	release := make(chan struct{})
	done := make(chan error, 1)

	go func() {
		runtime.LockOSThread()
		defer runtime.UnlockOSThread()
		kernel32 := syscall.NewLazyDLL("kernel32.dll")
		setThreadExecutionState := kernel32.NewProc("SetThreadExecutionState")
		r, _, callErr := setThreadExecutionState.Call(uintptr(esContinuous | esSystemRequired))
		if r == 0 {
			err := callErr
			if err == syscall.Errno(0) {
				err = fmt.Errorf("SetThreadExecutionState returned zero")
			}
			ready <- err
			done <- err
			return
		}
		ready <- nil
		<-release
		r, _, callErr = setThreadExecutionState.Call(uintptr(esContinuous))
		if r == 0 {
			err := callErr
			if err == syscall.Errno(0) {
				err = fmt.Errorf("SetThreadExecutionState release returned zero")
			}
			done <- err
			return
		}
		done <- nil
	}()

	if err := <-ready; err != nil {
		return nil, "", fmt.Errorf("acquire Windows power request: %w", err)
	}
	return func() error {
		close(release)
		return <-done
	}, "windows/SetThreadExecutionState", nil
}
