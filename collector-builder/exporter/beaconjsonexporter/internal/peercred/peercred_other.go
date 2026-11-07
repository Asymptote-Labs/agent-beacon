//go:build !linux

package peercred

import "net"

func lookupUID(_, _ *net.TCPAddr) (string, error) {
	return "", ErrUnsupported
}
