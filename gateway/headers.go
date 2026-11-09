package gateway

import "strings"

// IsSecretHeader reports whether a request header carries a credential, so an adapter leaves it
// out of Client.Headers: nothing downstream of an adapter ever needs a key or a cookie, and every
// record the pipeline writes would otherwise carry them.
func IsSecretHeader(name string) bool {
	name = strings.ToLower(name)
	switch name {
	case "authorization", "proxy-authorization", "cookie", "set-cookie", "x-api-key":
		return true
	}
	return strings.Contains(name, "api-key") || strings.Contains(name, "apikey") ||
		strings.Contains(name, "token") || strings.Contains(name, "secret")
}

// ClientHeaders copies headers for Client.Headers: names lowercased, credentials dropped, and
// values the gateway withheld (withheld != "" and equal to the value) left out, so a placeholder
// is never mistaken for the header's value.
func ClientHeaders(h map[string]string, withheld string) map[string]string {
	out := make(map[string]string, len(h))
	for k, v := range h {
		if IsSecretHeader(k) || (withheld != "" && v == withheld) {
			continue
		}
		out[strings.ToLower(k)] = v
	}
	return out
}
