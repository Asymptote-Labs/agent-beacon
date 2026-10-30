//go:build windows

package beaconingestextension

import (
	"os/user"
	"strings"
	"time"
)

// resolveName names a SID: DOMAIN\user, the form Windows hooks write to user.name. os/user asks
// LookupAccountSid, which consults a domain controller on a domain-joined machine and can wait on
// one that does not answer, so the lookup is bounded by nameLookupTimeout like id(1) on Unix: past
// it the event is named by SID only, and the cache retries later. Windows has no id(1) to fall
// back to.
func resolveName(sid string) string {
	done := make(chan string, 1)
	go func() {
		u, err := lookupAccount(sid)
		if err != nil || !validWindowsName(u.Username) {
			done <- ""
			return
		}
		done <- u.Username
	}()
	select {
	case name := <-done:
		return name
	case <-time.After(nameLookupTimeout):
		return ""
	}
}

// lookupAccount is os/user.LookupId; tests replace it.
var lookupAccount = user.LookupId

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
