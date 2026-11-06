//go:build !linux

package peercred

import "net"

func lookupUID(*net.TCPAddr) (string, error) {
	return "", ErrUnsupported
}
