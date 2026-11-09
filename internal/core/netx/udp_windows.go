//go:build windows

package netx

import (
	"net"
	"syscall"
	"unsafe"
)

// reportPortUnreachable turns back on, for the connected UDP socket conn,
// what Go's net package turns off for every UDP socket on Windows
// (SIO_UDP_CONNRESET): after an ICMP "port unreachable" for a datagram it
// sent, the next read fails with WSAECONNRESET, as it fails with
// ECONNREFUSED elsewhere (IsPortUnreachable). Go turns it off because an
// unconnected socket would fail its reads for any peer that went away; a
// connected one has only its target. Best effort.
func reportPortUnreachable(conn net.Conn) {
	sc, ok := conn.(syscall.Conn)
	if !ok {
		return
	}
	rc, err := sc.SyscallConn()
	if err != nil {
		return
	}
	_ = rc.Control(func(fd uintptr) {
		flag := uint32(1)
		var ret uint32
		_ = syscall.WSAIoctl(syscall.Handle(fd), syscall.SIO_UDP_CONNRESET, (*byte)(unsafe.Pointer(&flag)),
			uint32(unsafe.Sizeof(flag)), nil, 0, &ret, nil, 0)
	})
}
