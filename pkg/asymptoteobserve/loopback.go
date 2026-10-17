package asymptoteobserve

import (
	"net"
	"strings"
)

// IsLoopbackHost reports whether a request's Host header, with or without a port, names localhost
// or a loopback IP. A page that DNS-rebinds its own hostname to 127.0.0.1 is same-origin with a
// loopback server, but its requests still carry that hostname here.
func IsLoopbackHost(host string) bool {
	if h, _, err := net.SplitHostPort(host); err == nil {
		host = h
	}
	host = strings.Trim(host, "[]")
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}
