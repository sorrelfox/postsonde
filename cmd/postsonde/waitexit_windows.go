//go:build windows

package main

import (
	"os"
	"time"
)

// waitForExit waits until process pid has exited, at most timeout: the
// PostSonde a restart came from (relaunchEnv). On Windows a process
// handle can be waited on by any process.
func waitForExit(pid int, timeout time.Duration) {
	if pid <= 0 {
		return
	}
	p, err := os.FindProcess(pid)
	if err != nil {
		return // already gone
	}
	done := make(chan struct{})
	go func() {
		_, _ = p.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(timeout):
	}
}
