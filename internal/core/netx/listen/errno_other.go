//go:build !windows

package listen

import "syscall"

var (
	inUseErrnos       = []syscall.Errno{syscall.EADDRINUSE}
	accessErrnos      = []syscall.Errno{syscall.EACCES, syscall.EPERM}
	unavailableErrnos = []syscall.Errno{syscall.EADDRNOTAVAIL, syscall.EAFNOSUPPORT, syscall.EPROTONOSUPPORT}
)
