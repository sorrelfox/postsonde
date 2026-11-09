//go:build windows

package netx

import "syscall"

// Winsock reports its own error numbers, not the POSIX ones in syscall.
const (
	wsaeConnAborted syscall.Errno = 10053
	wsaeConnReset   syscall.Errno = 10054
	wsaeNetUnreach  syscall.Errno = 10051
	wsaeConnRefused syscall.Errno = 10061
	wsaeHostUnreach syscall.Errno = 10065
)

var (
	refusedErrnos     = []syscall.Errno{wsaeConnRefused, syscall.ECONNREFUSED}
	unreachableErrnos = []syscall.Errno{wsaeHostUnreach, wsaeNetUnreach, syscall.EHOSTUNREACH, syscall.ENETUNREACH}
	resetErrnos       = []syscall.Errno{wsaeConnReset, wsaeConnAborted, syscall.ECONNRESET, syscall.ECONNABORTED, syscall.EPIPE}
	// Winsock reports an ICMP "port unreachable" on a connected UDP
	// socket as WSAECONNRESET (reportPortUnreachable).
	portUnreachableErrnos = []syscall.Errno{wsaeConnReset, wsaeConnRefused, syscall.ECONNREFUSED}
)
