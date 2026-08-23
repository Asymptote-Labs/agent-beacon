package service

import (
	"errors"
	"os/user"
	"strings"
	"testing"
)

// Linger is the one install step whose reported result used to be inferred from the wrong signal.
// `loginctl enable-linger` can exit non-zero because a helper it spawned is missing while the
// enable itself lands, and it can exit zero without the state changing. What the user cares about
// is whether the collector survives logout, so these drive both signals independently and pin the
// outcome to the state read back from logind.
//
// Everything here is deterministic: loginctl is a stub and the systemd gate is stubbed, so the
// same branches run on macOS, on a Linux container without systemd, and on a systemd host,
// without touching the host's real logind state.

// fakeLoginctl replaces the loginctl runner and the systemd gate for one test.
//
// The linger field is read and written by the stub the way logind would: `show-user` reports it, and
// `enable-linger` sets it only when enableSets says the call actually took effect. That
// separation is the whole point -- it lets a test express "the command failed but linger is on"
// and "the command succeeded but linger is off", which are the two cases the old exit-code
// reading got wrong.
type fakeLoginctl struct {
	linger     bool
	enableSets bool
	enableOut  string
	enableErr  error
	calls      [][]string
}

func (f *fakeLoginctl) install(t *testing.T) {
	t.Helper()
	prevRun, prevInit := runLoginctlCommand, lingerSystemdPresent
	t.Cleanup(func() { runLoginctlCommand, lingerSystemdPresent = prevRun, prevInit })
	lingerSystemdPresent = func() bool { return true }
	runLoginctlCommand = func(args ...string) (string, error) {
		f.calls = append(f.calls, args)
		for _, arg := range args {
			switch arg {
			case "enable-linger":
				if f.enableSets {
					f.linger = true
				}
				return f.enableOut, f.enableErr
			case "show-user":
				if f.linger {
					return "yes\n", nil
				}
				return "no\n", nil
			}
		}
		return "", nil
	}
}

func (f *fakeLoginctl) called(verb string) bool {
	for _, args := range f.calls {
		for _, arg := range args {
			if arg == verb {
				return true
			}
		}
	}
	return false
}

// The core inversion: the outcome follows the state, not the exit code.
func TestEnableLingerReportsStateRatherThanExitCode(t *testing.T) {
	authRequired := "Failed to enable linger: Interactive authentication required."
	pkttyagent := "Failed to execute /usr/bin/pkttyagent: No such file or directory"

	for _, tc := range []struct {
		name        string
		fake        fakeLoginctl
		wantEnabled bool
		wantDetail  []string
		denyDetail  []string
	}{
		{
			// The ordinary success: privileged enough to set it, and logind confirms.
			name:        "applied and confirmed",
			fake:        fakeLoginctl{enableSets: true},
			wantEnabled: true,
			wantDetail:  []string{"enabled"},
		},
		{
			// The reported failure that is not one. loginctl exits non-zero because the
			// PolicyKit text agent it tried to spawn is missing, but linger is set regardless.
			// Reading the exit code here would report a gap that does not exist and send the
			// user off to run sudo for nothing.
			name:        "helper failed noisily but linger landed",
			fake:        fakeLoginctl{enableSets: true, enableOut: pkttyagent, enableErr: errors.New("exit status 1")},
			wantEnabled: true,
			wantDetail:  []string{"enabled"},
		},
		{
			// The inverse, and the one that produced a success transcript the next logout
			// disproved: nothing failed loudly, and linger is still off.
			name:        "zero exit that changed nothing",
			fake:        fakeLoginctl{enableSets: false},
			wantEnabled: false,
			wantDetail:  []string{"disabled", "no error"},
		},
		{
			// The reported Linux install. The user gets told what is actually wrong -- they
			// are not an administrator -- and never sees a path to a file they never asked
			// about and cannot usefully install.
			name:        "auth agent chatter is not the explanation",
			fake:        fakeLoginctl{enableOut: pkttyagent, enableErr: errors.New("exit status 1")},
			wantEnabled: false,
			wantDetail:  []string{"disabled", "administrator approval"},
			denyDetail:  []string{"pkttyagent"},
		},
		{
			// Chatter is filtered, not everything. A real reason from logind is the most
			// useful sentence available and must survive.
			name:        "a real reason survives the filter",
			fake:        fakeLoginctl{enableOut: pkttyagent + "\n" + authRequired, enableErr: errors.New("exit status 1")},
			wantEnabled: false,
			wantDetail:  []string{"Interactive authentication required"},
			denyDetail:  []string{"pkttyagent"},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fake := tc.fake
			fake.install(t)
			enabled, detail := EnableLinger("someone")
			if enabled != tc.wantEnabled {
				t.Errorf("EnableLinger enabled = %v, want %v (detail %q)", enabled, tc.wantEnabled, detail)
			}
			for _, want := range tc.wantDetail {
				if !strings.Contains(detail, want) {
					t.Errorf("detail %q should mention %q", detail, want)
				}
			}
			for _, deny := range tc.denyDetail {
				if strings.Contains(detail, deny) {
					t.Errorf("detail %q should not relay %q", detail, deny)
				}
			}
			if detail == "" {
				t.Error("every applicable outcome must report a detail")
			}
		})
	}
}

// --no-ask-password is the fix for the noise itself, not just for how it is reported: without it
// loginctl spawns a PolicyKit text agent, which is what wrote the unexplained pkttyagent line
// into the middle of a successful install transcript. It also writes to the controlling terminal
// rather than the stderr a caller captures, so suppressing the prompt is the only reliable way to
// keep it out of the transcript.
func TestEnableLingerDoesNotAskForAPassword(t *testing.T) {
	fake := fakeLoginctl{enableSets: true}
	fake.install(t)
	EnableLinger("someone")

	for _, args := range fake.calls {
		if args[0] == "show-user" {
			continue
		}
		if args[0] != "--no-ask-password" {
			t.Errorf("loginctl %v: --no-ask-password must come before the verb", args)
		}
	}
	if !fake.called("enable-linger") {
		t.Fatal("EnableLinger did not run enable-linger")
	}
}

// A remediation is a promise that running that exact command closes the gap. It belongs on the
// outcome only when linger is genuinely off and the user it applies to is known -- printing it
// after a success would be noise, and printing it when the username is unresolvable would name
// the wrong user.
func TestEnableLingerIfNeededAttachesRemediationOnlyWhenLingerIsOff(t *testing.T) {
	u, err := user.Current()
	if err != nil || u.Username == "" {
		t.Skip("needs a resolvable current user")
	}
	manager := Manager{UserMode: true, Kind: KindSystemd}

	t.Run("off", func(t *testing.T) {
		fake := fakeLoginctl{}
		fake.install(t)
		got := manager.EnableLingerIfNeeded()
		if !got.Applicable || got.Enabled {
			t.Fatalf("outcome = %+v, want applicable and disabled", got)
		}
		if want := "sudo loginctl enable-linger " + u.Username; got.Remediation != want {
			t.Errorf("remediation = %q, want %q", got.Remediation, want)
		}
	})

	t.Run("already on", func(t *testing.T) {
		fake := fakeLoginctl{linger: true}
		fake.install(t)
		got := manager.EnableLingerIfNeeded()
		if !got.Applicable || !got.Enabled {
			t.Fatalf("outcome = %+v, want applicable and enabled", got)
		}
		if got.Remediation != "" {
			t.Errorf("remediation = %q, want none once linger is on", got.Remediation)
		}
		// Nothing to do, so nothing should be attempted: an install that re-enables linger on
		// every run would ask for privileges it does not need.
		if fake.called("enable-linger") {
			t.Error("enable-linger should not run when linger is already on")
		}
	})
}

// Chatter filtering is line-based, so a reason that arrives alongside noise has to survive intact
// and a multi-line reason has to stay readable on the single line the caller prints.
func TestWithoutAuthAgentChatterKeepsEverythingElse(t *testing.T) {
	for _, tc := range []struct{ name, in, want string }{
		{"empty", "", ""},
		{"only chatter", "Failed to execute /usr/bin/pkttyagent: No such file or directory\n", ""},
		{"only agent registration chatter", "Error registering polkit-agent: whatever\n", ""},
		{"reason kept", "Failed to enable linger: Access denied.\n", "Failed to enable linger: Access denied."},
		{
			"reason kept alongside chatter",
			"Failed to execute /usr/bin/pkttyagent: No such file or directory\nAccess denied.\n",
			"Access denied.",
		},
		{"multiple lines joined", "first problem\nsecond problem\n", "first problem; second problem"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := withoutAuthAgentChatter(tc.in); got != tc.want {
				t.Errorf("withoutAuthAgentChatter(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

// The guard exists so linger reporting stays inert off systemd. Without it, the stubbed tests
// above would be the only thing standing between a macOS install and a logout warning about a
// service manager it does not use.
func TestLingerHelpersAreInertWithoutSystemd(t *testing.T) {
	prev := lingerSystemdPresent
	t.Cleanup(func() { lingerSystemdPresent = prev })
	lingerSystemdPresent = func() bool { return false }

	prevRun := runLoginctlCommand
	t.Cleanup(func() { runLoginctlCommand = prevRun })
	runLoginctlCommand = func(args ...string) (string, error) {
		t.Errorf("loginctl %v should not run without systemd as PID 1", args)
		return "", nil
	}

	if enabled, detail := EnableLinger("someone"); enabled || detail == "" {
		t.Errorf("EnableLinger = %v, %q; want a reported no-op", enabled, detail)
	}
	if LingerEnabled("someone") {
		t.Error("LingerEnabled should be false without systemd as PID 1")
	}
}

// The remediation is printed for the user to paste, so it has to be complete: the command, the
// privilege it needs, and the account it applies to, with nothing left to substitute.
func TestLingerRemediationIsRunnableAsPrinted(t *testing.T) {
	got := LingerRemediation("someone")
	for _, want := range []string{"sudo", "loginctl enable-linger", "someone"} {
		if !strings.Contains(got, want) {
			t.Errorf("LingerRemediation = %q, want it to contain %q", got, want)
		}
	}
	if strings.ContainsAny(got, "<>$") {
		t.Errorf("LingerRemediation = %q, want no placeholder left for the user to fill in", got)
	}
}
