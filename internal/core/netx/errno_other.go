//go:build !windows

package netx

import "syscall"

var (
	refusedErrnos         = []syscall.Errno{syscall.ECONNREFUSED}
	unreachableErrnos     = []syscall.Errno{syscall.EHOSTUNREACH, syscall.ENETUNREACH}
	resetErrnos           = []syscall.Errno{syscall.ECONNRESET, syscall.ECONNABORTED, syscall.EPIPE}
	portUnreachableErrnos = []syscall.Errno{syscall.ECONNREFUSED}
)
