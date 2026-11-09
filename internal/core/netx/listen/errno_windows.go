//go:build windows

package listen

import "syscall"

// Winsock reports its own error numbers, not the POSIX ones in syscall.
const (
	wsaeAccess         syscall.Errno = 10013
	wsaeProtoNoSupport syscall.Errno = 10043
	wsaeAfNoSupport    syscall.Errno = 10047
	wsaeAddrInUse      syscall.Errno = 10048
	wsaeAddrNotAvail   syscall.Errno = 10049
)

var (
	inUseErrnos       = []syscall.Errno{wsaeAddrInUse, syscall.EADDRINUSE}
	accessErrnos      = []syscall.Errno{wsaeAccess, syscall.EACCES, syscall.EPERM}
	unavailableErrnos = []syscall.Errno{wsaeAddrNotAvail, wsaeAfNoSupport, wsaeProtoNoSupport, syscall.EADDRNOTAVAIL, syscall.EAFNOSUPPORT, syscall.EPROTONOSUPPORT}
)
