//go:build !windows

package main

import (
	"errors"
	"syscall"
	"time"
)

// waitForExit waits until process pid has exited, at most timeout: the
// PostSonde a restart came from (relaunchEnv). The process is not this
// one's parent's child any more, so it is polled with signal 0.
func waitForExit(pid int, timeout time.Duration) {
	if pid <= 0 {
		return
	}
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if err := syscall.Kill(pid, 0); errors.Is(err, syscall.ESRCH) {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
}
