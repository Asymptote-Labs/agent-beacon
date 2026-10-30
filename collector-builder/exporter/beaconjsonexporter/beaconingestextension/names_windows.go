//go:build windows

package beaconingestextension

import (
	"strings"
	"time"

	"golang.org/x/sys/windows"
)

// resolveName names a SID: DOMAIN\user, the form Windows hooks write to user.name. It asks
// LookupAccountSid, which consults a domain controller on a domain-joined machine and can wait on
// one that does not answer, so the lookup is bounded by nameLookupTimeout like id(1) on Unix: past
// it the event is named by SID only, and the cache retries later. Windows has no id(1) to fall
// back to.
func resolveName(sid string) string {
	done := make(chan string, 1)
	go func() {
		name, err := lookupAccount(sid)
		if err != nil || !validWindowsName(name) {
			done <- ""
			return
		}
		done <- name
	}()
	select {
	case name := <-done:
		return name
	case <-time.After(nameLookupTimeout):
		return ""
	}
}

// lookupAccount names a SID with LookupAccountSid alone; tests replace it. Not os/user.LookupId:
// that also reads the account's full name (NetUserGetInfo) and profile directory and refuses
// anything that is not SidTypeUser, so a Microsoft Entra ID account, a virtual service account
// or SYSTEM, which LookupAccountSid names perfectly well, would come out unnamed.
var lookupAccount = func(sid string) (string, error) {
	s, err := windows.StringToSid(sid)
	if err != nil {
		return "", err
	}
	account, domain, _, err := s.LookupAccount("")
	if err != nil {
		return "", err
	}
	if domain == "" {
		return account, nil
	}
	return domain + `\` + account, nil
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
