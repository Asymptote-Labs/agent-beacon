//go:build windows

package writer

import (
	"os/exec"
	"strings"
	"testing"

	"golang.org/x/sys/windows"
)

func TestClassifyACEReadsTheEntryShapesThatDecideAccess(t *testing.T) {
	const (
		writeData  = uint32(windows.FILE_WRITE_DATA)
		appendData = uint32(windows.FILE_APPEND_DATA)
		readData   = uint32(windows.FILE_READ_DATA)
	)

	cases := []struct {
		name       string
		aceType    uint8
		aceFlags   uint8
		mask       uint32
		matchesSID bool
		want       aceVerdict
	}{
		{
			name:       "the grant install writes",
			aceType:    windows.ACCESS_ALLOWED_ACE_TYPE,
			mask:       writeData | appendData,
			matchesSID: true,
			want:       aceAllowsWrite,
		},
		{
			// The whole reason this is not a substring search for a rendered name.
			name:       "another principal's full control says nothing about INTERACTIVE",
			aceType:    windows.ACCESS_ALLOWED_ACE_TYPE,
			mask:       writeData | appendData,
			matchesSID: false,
			want:       aceIrrelevant,
		},
		{
			// A hook that cannot create the log fails as surely as one that cannot append to it,
			// so half the mask is not half a pass.
			name:       "append without write cannot create the log",
			aceType:    windows.ACCESS_ALLOWED_ACE_TYPE,
			mask:       appendData,
			matchesSID: true,
			want:       aceIrrelevant,
		},
		{
			name:       "write without append cannot extend the log",
			aceType:    windows.ACCESS_ALLOWED_ACE_TYPE,
			mask:       writeData,
			matchesSID: true,
			want:       aceIrrelevant,
		},
		{
			name:       "read access is not write access",
			aceType:    windows.ACCESS_ALLOWED_ACE_TYPE,
			mask:       readData,
			matchesSID: true,
			want:       aceIrrelevant,
		},
		{
			name:       "generic write subsumes both bits",
			aceType:    windows.ACCESS_ALLOWED_ACE_TYPE,
			mask:       windows.GENERIC_WRITE,
			matchesSID: true,
			want:       aceAllowsWrite,
		},
		{
			// The directory itself must be writable: a hook creates the log when it is missing.
			name:       "an inherit-only grant does not make this directory writable",
			aceType:    windows.ACCESS_ALLOWED_ACE_TYPE,
			aceFlags:   inheritOnlyACE,
			mask:       writeData | appendData,
			matchesSID: true,
			want:       aceIrrelevant,
		},
		{
			name:       "a deny of either bit is a deny",
			aceType:    windows.ACCESS_DENIED_ACE_TYPE,
			mask:       appendData,
			matchesSID: true,
			want:       aceDeniesWrite,
		},
		{
			name:       "a deny of unrelated rights is not a deny of write",
			aceType:    windows.ACCESS_DENIED_ACE_TYPE,
			mask:       readData,
			matchesSID: true,
			want:       aceIrrelevant,
		},
		{
			name:       "an inherit-only deny does not apply to this directory",
			aceType:    windows.ACCESS_DENIED_ACE_TYPE,
			aceFlags:   inheritOnlyACE,
			mask:       writeData | appendData,
			matchesSID: true,
			want:       aceIrrelevant,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := classifyACE(tc.aceType, tc.aceFlags, tc.mask, tc.matchesSID)
			if got != tc.want {
				t.Fatalf("classifyACE = %v, want %v", got, tc.want)
			}
		})
	}
}

// TestGrantAndReadBackAgree exercises the halves against each other on a real directory.
//
// They are written against different interfaces -- the grant shells out to icacls, the read walks
// the DACL through the security APIs -- so nothing but a test like this catches them drifting
// apart. A reader that silently answered "no" to a directory the granter had just fixed would make
// doctor report a working endpoint as broken, and it would do so only on machines nobody develops
// on.
//
// Every step here needs administrator rights, which the endpoint install already requires and the
// Windows CI runner already has. Skipped rather than failed elsewhere: a contributor running the
// suite unelevated should not see a red suite for a check their shell cannot perform.
func TestGrantAndReadBackAgree(t *testing.T) {
	dir := t.TempDir()

	// Strip inheritance so the starting state is known: administrators only, no INTERACTIVE. This
	// is what makes the negative half real -- without it the answer could be "true" before the
	// grant, and a reader hardcoded to return true would pass.
	if out, err := exec.Command("icacls", dir, "/inheritance:r", "/grant", `*S-1-5-32-544:(OI)(CI)(F)`).CombinedOutput(); err != nil {
		t.Skipf("could not take ownership of the ACL on %s (needs an elevated shell): %v: %s",
			dir, err, strings.TrimSpace(string(out)))
	}

	if writable, err := interactiveUsersCanWrite(dir); err != nil {
		t.Fatalf("read the ACL before granting: %v", err)
	} else if writable {
		t.Fatal("a directory granting only administrators reported interactive users can write; " +
			"the reader cannot distinguish a granted directory from an ungranted one")
	}

	if err := grantInteractiveUsersWrite(dir); err != nil {
		t.Fatalf("grant interactive write access: %v", err)
	}

	if writable, err := interactiveUsersCanWrite(dir); err != nil {
		t.Fatalf("read the ACL after granting: %v", err)
	} else if !writable {
		t.Fatal("the directory the granter just fixed reads as not writable by interactive users; " +
			"doctor would report a healthy endpoint as broken")
	}

	// A deny entry outranks the grant, and doctor must say so: hooks would fail here despite the
	// allow entry still being present.
	if out, err := exec.Command("icacls", dir, "/deny", `*S-1-5-4:(WD)`).CombinedOutput(); err != nil {
		t.Fatalf("add a deny entry: %v: %s", err, strings.TrimSpace(string(out)))
	}
	if writable, err := interactiveUsersCanWrite(dir); err != nil {
		t.Fatalf("read the ACL after denying: %v", err)
	} else if writable {
		t.Fatal("an explicit deny of write access reported as writable; " +
			"the grant was read without the deny that overrides it")
	}
}
