//go:build windows

package beaconingestextension

import (
	"os/user"
	"strings"
)

// resolveName names a SID: DOMAIN\user, the form Windows hooks write to user.name. os/user asks
// LookupAccountSid, which consults the domain on a domain-joined machine; Windows has no id(1) to
// fall back to.
func resolveName(sid string) string {
	u, err := user.LookupId(sid)
	if err != nil || !validWindowsName(u.Username) {
		return ""
	}
	return u.Username
}

// validWindowsName is validName with spaces allowed inside the name, as Windows account names
// allow them ("CONTOSO\Jane Doe"); still no control characters, nothing at either end, bounded.
func validWindowsName(name string) bool {
	if name == "" || len(name) > 256 || strings.TrimSpace(name) != name {
		return false
	}
	for _, r := range name {
		if r < ' ' || r == 0x7f {
			return false
		}
	}
	return true
}
