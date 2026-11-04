//go:build windows

package asymptoteobserve

import (
	"io"
	"os"
	"runtime"
	"strings"
	"unsafe"

	"golang.org/x/sys/windows"
)

// logAccessMarkerTrustedSID is one more owner a marker may have besides SYSTEM and Administrators;
// tests that cannot create an Administrators-owned file set their own SID.
var logAccessMarkerTrustedSID = ""

// markerWriteRights are the rights no account other than SYSTEM, Administrators and TrustedInstaller
// may hold on the marker: anything that could change what it says, replace it, or re-secure it.
const markerWriteRights = windows.FILE_WRITE_DATA | windows.FILE_APPEND_DATA | windows.FILE_WRITE_EA |
	windows.FILE_WRITE_ATTRIBUTES | windows.DELETE | windows.WRITE_DAC | windows.WRITE_OWNER |
	windows.GENERIC_WRITE | windows.GENERIC_ALL | windows.MAXIMUM_ALLOWED | windows.ACCESS_SYSTEM_SECURITY

// readLogAccessMarker reads the marker at path when it can only have been written by SYSTEM or an
// administrator: a plain file (not a link, not hard-linked elsewhere), owned by SYSTEM or
// Administrators, whose DACL gives no other account any right to change it. Anything else reads as
// no marker, the way the Unix reader ignores one that root does not own.
func readLogAccessMarker(path string) string {
	p, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return ""
	}
	h, err := windows.CreateFile(p, windows.GENERIC_READ|windows.READ_CONTROL,
		windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE, nil,
		windows.OPEN_EXISTING, windows.FILE_FLAG_OPEN_REPARSE_POINT, 0)
	if err != nil {
		return ""
	}
	f := os.NewFile(uintptr(h), path)
	defer f.Close()
	var info windows.ByHandleFileInformation
	if err := windows.GetFileInformationByHandle(h, &info); err != nil {
		return ""
	}
	if info.FileAttributes&(windows.FILE_ATTRIBUTE_REPARSE_POINT|windows.FILE_ATTRIBUTE_DIRECTORY) != 0 || info.NumberOfLinks != 1 {
		return ""
	}
	if !markerSecurityTrusted(h) {
		return ""
	}
	data, err := io.ReadAll(io.LimitReader(f, maxLogAccessMarkerBytes+1))
	if err != nil || len(data) > maxLogAccessMarkerBytes {
		return ""
	}
	return strings.TrimSpace(string(data))
}

func markerSecurityTrusted(h windows.Handle) bool {
	sd, err := windows.GetSecurityInfo(h, windows.SE_FILE_OBJECT, windows.OWNER_SECURITY_INFORMATION|windows.DACL_SECURITY_INFORMATION)
	if err != nil {
		return false
	}
	defer runtime.KeepAlive(sd)
	owner, _, err := sd.Owner()
	if err != nil || owner == nil || !markerTrustedPrincipal(owner.String()) {
		return false
	}
	dacl, _, err := sd.DACL()
	if err != nil || dacl == nil {
		return false // no DACL grants everyone everything
	}
	for i := uint32(0); i < uint32(dacl.AceCount); i++ {
		var ace *windows.ACCESS_ALLOWED_ACE
		if err := windows.GetAce(dacl, i, &ace); err != nil {
			return false
		}
		switch ace.Header.AceType {
		case windows.ACCESS_DENIED_ACE_TYPE:
			continue
		case windows.ACCESS_ALLOWED_ACE_TYPE:
			sid := (*windows.SID)(unsafe.Pointer(&ace.SidStart)).String()
			if markerTrustedPrincipal(sid) {
				continue
			}
			if ace.Header.AceFlags&windows.INHERIT_ONLY_ACE == 0 && uint32(ace.Mask)&markerWriteRights != 0 {
				return false
			}
		default:
			return false // an entry this does not model is not trusted
		}
	}
	return true
}

func markerTrustedPrincipal(sid string) bool {
	switch sid {
	case "S-1-5-18", "S-1-5-32-544", "S-1-5-80-956008885-3418522649-1831038044-1853292631-2271478464":
		return true
	}
	return logAccessMarkerTrustedSID != "" && strings.EqualFold(sid, logAccessMarkerTrustedSID)
}
