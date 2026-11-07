//go:build !linux && !darwin && !windows

package peercred

import "net"

func lookupUID(_, _ *net.TCPAddr) (string, error) {
	return "", ErrUnsupported
}
