//go:build !windows

package netx

import "net"

// reportPortUnreachable does nothing: a connected UDP socket reports an
// ICMP "port unreachable" (ECONNREFUSED) on its own here.
func reportPortUnreachable(net.Conn) {}
