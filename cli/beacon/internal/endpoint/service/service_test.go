package service

import (
	"errors"
	"fmt"
	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/testenv"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestPlistContainsLaunchdContract(t *testing.T) {
	content := plist(UserLabel, "/tmp/otelcol", "/tmp/otelcol.yaml")

	for _, want := range []string{
		"<string>" + UserLabel + "</string>",
		"<string>/tmp/otelcol</string>",
		"<string>--config</string>",
		"<string>/tmp/otelcol.yaml</string>",
		"<key>RunAtLoad</key>",
		"<key>KeepAlive</key>",
		"<string>/tmp/" + UserLabel + ".out</string>",
		"<string>/tmp/" + UserLabel + ".err</string>",
	} {
		if !strings.Contains(content, want) {
			t.Fatalf("plist missing %q:\n%s", want, content)
		}
	}
}

func TestRunLaunchctlWithContextExplainsBootstrapIOError(t *testing.T) {
	oldRun := runLaunchctlCommand
	runLaunchctlCommand = func(args ...string) (string, error) {
		return "Bootstrap failed: 5: Input/output error", errors.New("exit status 5")
	}
	t.Cleanup(func() {
		runLaunchctlCommand = oldRun
	})

	err := runLaunchctlWithContext("gui/501", UserLabel, "/Users/test/Library/LaunchAgents/"+UserLabel+".plist", "bootstrap", "gui/501", "/Users/test/Library/LaunchAgents/"+UserLabel+".plist")
	if err == nil {
		t.Fatal("runLaunchctlWithContext returned nil, want error")
	}
	text := err.Error()
	for _, want := range []string{
		"label " + UserLabel,
		"domain gui/501",
		"plist /Users/test/Library/LaunchAgents/" + UserLabel + ".plist",
		"Verify the collector binary",
		"launchctl bootout gui/501/" + UserLabel,
		"log show --predicate",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("error missing %q:\n%s", want, text)
		}
	}
}

func TestLoadLaunchdJobReloadsAlreadyBootstrappedJob(t *testing.T) {
	var calls []string
	oldRun := runLaunchctlCommand
	runLaunchctlCommand = func(args ...string) (string, error) {
		calls = append(calls, strings.Join(args, " "))
		switch len(calls) {
		case 1:
			return "Bootstrap failed: 5: already bootstrapped", errors.New("exit status 5")
		case 2, 3:
			return "", nil
		default:
			return "", errors.New("unexpected launchctl call")
		}
	}
	t.Cleanup(func() {
		runLaunchctlCommand = oldRun
	})

	if err := loadLaunchdJob("gui/501", UserLabel, "/Users/test/Library/LaunchAgents/"+UserLabel+".plist"); err != nil {
		t.Fatalf("loadLaunchdJob returned error: %v", err)
	}
	if len(calls) != 3 {
		t.Fatalf("launchctl calls = %#v, want bootstrap/bootout/bootstrap", calls)
	}
	if !strings.HasPrefix(calls[0], "bootstrap gui/") || !strings.Contains(calls[1], "bootout gui/") || !strings.HasPrefix(calls[2], "bootstrap gui/") {
		t.Fatalf("unexpected launchctl call sequence: %#v", calls)
	}
}

func TestLoadLaunchdJobReloadsBootstrapIOErrorWhenJobPrints(t *testing.T) {
	var calls []string
	oldRun := runLaunchctlCommand
	runLaunchctlCommand = func(args ...string) (string, error) {
		calls = append(calls, strings.Join(args, " "))
		switch strings.Join(args, " ") {
		case "bootstrap system /Library/LaunchDaemons/" + SystemLabel + ".plist":
			if len(calls) == 1 {
				return "Bootstrap failed: 5: Input/output error", errors.New("exit status 5")
			}
			return "", nil
		case "print system/" + SystemLabel:
			return "state = running\npid = 123\n", nil
		case "bootout system/" + SystemLabel:
			return "", nil
		default:
			return "", fmt.Errorf("unexpected launchctl call: %s", strings.Join(args, " "))
		}
	}
	t.Cleanup(func() {
		runLaunchctlCommand = oldRun
	})

	if err := loadLaunchdJob("system", SystemLabel, "/Library/LaunchDaemons/"+SystemLabel+".plist"); err != nil {
		t.Fatalf("loadLaunchdJob returned error: %v", err)
	}
	if len(calls) != 4 {
		t.Fatalf("launchctl calls = %#v, want bootstrap/print/bootout/bootstrap", calls)
	}
}

func TestLoadLaunchdJobTreatsPostBootstrapLoadedStateAsSuccess(t *testing.T) {
	var calls []string
	oldRun := runLaunchctlCommand
	runLaunchctlCommand = func(args ...string) (string, error) {
		calls = append(calls, strings.Join(args, " "))
		switch strings.Join(args, " ") {
		case "bootstrap system /Library/LaunchDaemons/" + SystemLabel + ".plist":
			return "Bootstrap failed: 5: Input/output error", errors.New("exit status 5")
		case "print system/" + SystemLabel:
			return "state = running\npid = 123\n", nil
		case "bootout system/" + SystemLabel:
			return "", nil
		default:
			return "", fmt.Errorf("unexpected launchctl call: %s", strings.Join(args, " "))
		}
	}
	t.Cleanup(func() {
		runLaunchctlCommand = oldRun
	})

	if err := loadLaunchdJob("system", SystemLabel, "/Library/LaunchDaemons/"+SystemLabel+".plist"); err != nil {
		t.Fatalf("loadLaunchdJob returned error: %v", err)
	}
	if len(calls) != 5 {
		t.Fatalf("launchctl calls = %#v, want bootstrap/print/bootout/bootstrap/print", calls)
	}
}

func TestLoadLaunchdJobReturnsBootoutFailureWhenReloadFails(t *testing.T) {
	oldRun := runLaunchctlCommand
	runLaunchctlCommand = func(args ...string) (string, error) {
		switch args[0] {
		case "bootstrap":
			return "Bootstrap failed: 5: already bootstrapped", errors.New("exit status 5")
		case "bootout":
			return "Boot-out failed", errors.New("exit status 5")
		default:
			return "", fmt.Errorf("unexpected launchctl call: %s", strings.Join(args, " "))
		}
	}
	t.Cleanup(func() {
		runLaunchctlCommand = oldRun
	})

	err := loadLaunchdJob("gui/501", UserLabel, "/Users/test/Library/LaunchAgents/"+UserLabel+".plist")
	if err == nil || !strings.Contains(err.Error(), "Boot-out failed") {
		t.Fatalf("loadLaunchdJob error = %v, want bootout failure", err)
	}
}

func TestManagerRestartKickstartsLoadedJob(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("launchd restart is macOS-only")
	}
	var calls []string
	oldRun := runLaunchctlCommand
	runLaunchctlCommand = func(args ...string) (string, error) {
		calls = append(calls, strings.Join(args, " "))
		return "", nil
	}
	t.Cleanup(func() {
		runLaunchctlCommand = oldRun
	})

	if err := (Manager{UserMode: false}).Restart(); err != nil {
		t.Fatalf("Restart returned error: %v", err)
	}
	want := "kickstart -k system/" + SystemLabel
	if len(calls) != 1 || calls[0] != want {
		t.Fatalf("launchctl calls = %#v, want %q", calls, want)
	}
}

func TestManagerRestartLoadsMissingJob(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("launchd restart is macOS-only")
	}
	var calls []string
	oldRun := runLaunchctlCommand
	runLaunchctlCommand = func(args ...string) (string, error) {
		calls = append(calls, strings.Join(args, " "))
		switch args[0] {
		case "kickstart":
			return "Could not find service \"system/" + SystemLabel + "\" in domain for system", errors.New("exit status 113")
		case "bootstrap":
			return "", nil
		default:
			return "", fmt.Errorf("unexpected launchctl call: %s", strings.Join(args, " "))
		}
	}
	t.Cleanup(func() {
		runLaunchctlCommand = oldRun
	})

	if err := (Manager{UserMode: false}).Restart(); err != nil {
		t.Fatalf("Restart returned error: %v", err)
	}
	if len(calls) != 2 ||
		calls[0] != "kickstart -k system/"+SystemLabel ||
		calls[1] != "bootstrap system /Library/LaunchDaemons/"+SystemLabel+".plist" {
		t.Fatalf("launchctl calls = %#v, want kickstart/bootstrap", calls)
	}
}

func TestServiceDomainUserAndSystem(t *testing.T) {
	if got, want := serviceDomain(false), "system"; got != want {
		t.Fatalf("serviceDomain(system) = %q, want %q", got, want)
	}
	user := serviceDomain(true)
	if !strings.HasPrefix(user, "gui/") {
		t.Fatalf("serviceDomain(user) = %q, want gui/<uid> prefix", user)
	}
	if want := fmt.Sprintf("gui/%d", os.Getuid()); user != want {
		t.Fatalf("serviceDomain(user) = %q, want %q", user, want)
	}
}

func TestLaunchdIsInertOffDarwin(t *testing.T) {
	if runtime.GOOS == "darwin" {
		t.Skip("launchd is usable here")
	}
	// The old contract was "Load and Unload both return nil off darwin", which encoded
	// "Linux does nothing" and hid the fact that nothing was ever installed. The contract
	// now distinguishes the two: asking to load a launchd service where launchd does not
	// exist is an error, while Unload stays forgiving so uninstall and repair can call it
	// speculatively on any host. launchctl must not be invoked either way.
	manager := Manager{UserMode: true, Kind: KindLaunchd}
	called := false
	oldRun := runLaunchctlCommand
	runLaunchctlCommand = func(args ...string) (string, error) {
		called = true
		return "", errors.New("launchctl should not run off darwin")
	}
	t.Cleanup(func() { runLaunchctlCommand = oldRun })

	if err := manager.Load(); err == nil {
		t.Fatal("Load off darwin should report that launchd is unavailable, not silently succeed")
	}
	if err := manager.Unload(); err != nil {
		t.Fatalf("Unload off darwin = %v, want nil so uninstall can call it unconditionally", err)
	}
	if called {
		t.Fatal("launchctl was invoked off darwin")
	}
}

// Supervised mode is what Linux actually falls back to, so its lifecycle needs real coverage
// rather than the old "does nothing" assertion.
func TestSupervisedLifecycleStartsAndStopsAProcess(t *testing.T) {
	home := t.TempDir()
	testenv.SetHome(t, home)
	m := Manager{UserMode: true, Kind: KindSupervised}

	// Loading before install must explain itself rather than silently doing nothing.
	if err := m.Load(); err == nil {
		t.Fatal("Load with nothing recorded should error")
	}

	// A stand-in collector: long-lived, harmless, and tolerant of the arguments the loader
	// actually passes.
	//
	// `sleep` itself cannot be used, though it was. The loader starts the recorded program as
	// `<program> --config <path>`, which every sleep implementation rejects as an invalid interval,
	// so the child exited immediately and the assertion below only passed when it won a race
	// against the process being reaped. That made it pass on macOS and fail on a Linux runner.
	//
	// Nor can a shell script, which is what replaced it: Windows has no shebangs and decides
	// executability by extension, so the stub could not be started there at all -- leaving process
	// detach, graceful termination and liveness untested on the one platform where all three are
	// newly written. See stubCollectorPath.
	stub := stubCollectorPath(t)
	if _, err := m.WriteUnit(stub, "300"); err != nil {
		t.Fatalf("WriteUnit: %v", err)
	}
	if err := m.Load(); err != nil {
		t.Fatalf("Load: %v", err)
	}
	st := m.Status()
	if !st.Running {
		t.Fatalf("expected a running supervised process, got %#v", st)
	}
	if err := m.Unload(); err != nil {
		t.Fatalf("Unload: %v", err)
	}
	if st := m.Status(); st.Running {
		t.Fatalf("expected the process stopped after Unload, got %#v", st)
	}
	// Unload must stay idempotent, since uninstall may call it more than once.
	if err := m.Unload(); err != nil {
		t.Fatalf("second Unload should be a no-op, got %v", err)
	}
}

func TestLaunchctlGuidance(t *testing.T) {
	if got := launchctlGuidance("some unrelated launchctl output", "gui/501", UserLabel); got != "" {
		t.Fatalf("guidance for unrelated output = %q, want empty", got)
	}

	withTarget := launchctlGuidance("Bootstrap failed: 5", "gui/501", UserLabel)
	if !strings.Contains(withTarget, "launchctl bootout gui/501/"+UserLabel) {
		t.Fatalf("guidance missing domain/label target: %q", withTarget)
	}

	fallback := launchctlGuidance("Input/output error", "", "")
	if !strings.Contains(fallback, "the Beacon launchd job") {
		t.Fatalf("guidance missing generic target fallback: %q", fallback)
	}
}

// Backends are pinned explicitly in these tests rather than relying on detection, so launchd
// path and rendering logic stays covered when the suite runs on Linux CI, and vice versa.

func TestLaunchdLabelAndUnitPath(t *testing.T) {
	home := t.TempDir()
	testenv.SetHome(t, home)

	user := Manager{UserMode: true, Kind: KindLaunchd}
	if got := user.Label(); got != UserLabel {
		t.Fatalf("user label = %q, want %q", got, UserLabel)
	}
	userPath, err := user.UnitPath()
	if err != nil {
		t.Fatalf("user UnitPath returned error: %v", err)
	}
	if want := filepath.Join(home, "Library", "LaunchAgents", UserLabel+".plist"); userPath != want {
		t.Fatalf("user plist path = %q, want %q", userPath, want)
	}

	system := Manager{Kind: KindLaunchd}
	if got := system.Label(); got != SystemLabel {
		t.Fatalf("system label = %q, want %q", got, SystemLabel)
	}
	systemPath, err := system.UnitPath()
	if err != nil {
		t.Fatalf("system UnitPath returned error: %v", err)
	}
	if want := filepath.Join("/Library/LaunchDaemons", SystemLabel+".plist"); systemPath != want {
		t.Fatalf("system plist path = %q, want %q", systemPath, want)
	}
}

func TestSystemdLabelAndUnitPath(t *testing.T) {
	home := t.TempDir()
	testenv.SetHome(t, home)

	user := Manager{UserMode: true, Kind: KindSystemd}
	if got := user.Label(); got != SystemdUserUnit {
		t.Fatalf("user unit = %q, want %q", got, SystemdUserUnit)
	}
	userPath, err := user.UnitPath()
	if err != nil {
		t.Fatalf("user UnitPath returned error: %v", err)
	}
	if want := filepath.Join(home, ".config", "systemd", "user", SystemdUserUnit); userPath != want {
		t.Fatalf("user unit path = %q, want %q", userPath, want)
	}

	systemPath, err := (Manager{Kind: KindSystemd}).UnitPath()
	if err != nil {
		t.Fatalf("system UnitPath returned error: %v", err)
	}
	if want := filepath.Join("/etc/systemd/system", SystemdSystemUnit); systemPath != want {
		t.Fatalf("system unit path = %q, want %q", systemPath, want)
	}
}

// The systemd unit has to preserve the behaviour the launchd plist provided, or a Linux
// install silently loses restart-on-crash and start-at-boot.
func TestSystemdUnitPreservesLaunchdSemantics(t *testing.T) {
	content := unitFile("/opt/beacon/bin/beacon-otelcol", "/etc/beacon/endpoint/otelcol.yaml", false)
	for _, want := range []string{
		// Quoted, so systemd takes each path as one argument -- see systemdArg.
		`ExecStart="/opt/beacon/bin/beacon-otelcol" --config "/etc/beacon/endpoint/otelcol.yaml"`,
		"Restart=always",             // KeepAlive
		"WantedBy=multi-user.target", // RunAtLoad
		"StandardOutput=journal",     // replaces /tmp/<label>.out
		"User=root",
	} {
		if !strings.Contains(content, want) {
			t.Fatalf("system unit missing %q:\n%s", want, content)
		}
	}

	userUnit := unitFile("/usr/bin/beacon-otelcol", "/home/u/.beacon/endpoint/otelcol.yaml", true)
	if !strings.Contains(userUnit, "WantedBy=default.target") {
		t.Fatalf("user unit should target default.target:\n%s", userUnit)
	}
	if strings.Contains(userUnit, "User=root") {
		t.Fatalf("user unit must not force root:\n%s", userUnit)
	}
}

// systemctl takes --user for user-scope units; getting this wrong would operate on the wrong
// unit entirely.
func TestSystemctlArgsScoping(t *testing.T) {
	if got := systemctlArgs(true, "is-active", "u"); got[0] != "--user" {
		t.Fatalf("user scope should pass --user first, got %v", got)
	}
	if got := systemctlArgs(false, "is-active", "u"); got[0] == "--user" {
		t.Fatalf("system scope must not pass --user, got %v", got)
	}
}

func TestSystemdStatusReadsEnabledAndActive(t *testing.T) {
	old := runSystemctlCommand
	t.Cleanup(func() { runSystemctlCommand = old })

	runSystemctlCommand = func(args ...string) (string, error) {
		for _, a := range args {
			switch a {
			case "is-enabled":
				return "enabled\n", nil
			case "is-active":
				return "active\n", nil
			}
		}
		return "", nil
	}
	st := systemdBackend{}.status(false)
	if !st.Loaded || !st.Running {
		t.Fatalf("enabled+active should be loaded and running, got %#v", st)
	}

	// A unit that exists but is stopped must report loaded-but-not-running, not absent.
	runSystemctlCommand = func(args ...string) (string, error) {
		for _, a := range args {
			switch a {
			case "is-enabled":
				return "enabled\n", nil
			case "is-active":
				return "inactive\n", nil
			}
		}
		return "", nil
	}
	st = systemdBackend{}.status(false)
	if !st.Loaded || st.Running {
		t.Fatalf("enabled+inactive should be loaded, not running, got %#v", st)
	}
}

// Unload is called speculatively by uninstall and repair, so an absent unit must not be an
// error or those paths would fail on a clean host.
func TestSystemdUnloadToleratesMissingUnit(t *testing.T) {
	old := runSystemctlCommand
	t.Cleanup(func() { runSystemctlCommand = old })
	runSystemctlCommand = func(args ...string) (string, error) {
		return "Failed to stop beacon-collector.service: Unit beacon-collector.service not loaded.", errors.New("exit status 5")
	}
	if err := (systemdBackend{}).unload(false); err != nil {
		t.Fatalf("unload should tolerate a missing unit, got %v", err)
	}
}

func TestSystemdUnitMissingRecognizesSystemdPhrasings(t *testing.T) {
	for _, out := range []string{
		"Unit beacon-collector.service not loaded.",
		"Unit beacon-collector.service could not be found.",
		"Failed to open /etc/systemd/system/x.service: No such file or directory",
	} {
		if !systemdUnitMissing(out) {
			t.Errorf("should recognize as missing: %q", out)
		}
	}
	if systemdUnitMissing("Job for beacon-collector.service failed") {
		t.Error("a genuine failure must not be treated as a missing unit")
	}
}

// Supervised mode is the container/CI fallback. It must be usable everywhere, since that is
// the whole point of having it.
func TestSupervisedIsAlwaysAvailable(t *testing.T) {
	if !(Manager{Kind: KindSupervised}).Available() {
		t.Fatal("supervised mode must be available on every platform")
	}
}

func TestSupervisedRecordsAndReportsState(t *testing.T) {
	home := t.TempDir()
	testenv.SetHome(t, home)
	m := Manager{UserMode: true, Kind: KindSupervised}

	if st := m.Status(); st.Loaded || st.Running {
		t.Fatalf("nothing installed yet, got %#v", st)
	}

	path, err := m.WriteUnit("/bin/true", "/tmp/otelcol.yaml")
	if err != nil {
		t.Fatalf("WriteUnit: %v", err)
	}
	if filepath.Base(path) != "collector.pid" {
		t.Fatalf("supervised state should be a pidfile, got %q", path)
	}
	st := m.Status()
	if !st.Loaded {
		t.Fatalf("after WriteUnit the service should be recorded: %#v", st)
	}
	if st.Running {
		t.Fatalf("WriteUnit must not start anything: %#v", st)
	}
	// The absence of a supervisor is a real difference from launchd/systemd and must be
	// stated rather than implied.
	if _ = st.Message; !strings.Contains(m.Status().Message, "not running") {
		t.Fatalf("status message should say it is not running, got %q", st.Message)
	}
}

func TestParseKindAcceptsAliasesAndRejectsGarbage(t *testing.T) {
	for in, want := range map[string]Kind{
		"": KindAuto, "auto": KindAuto, "launchd": KindLaunchd,
		"systemd": KindSystemd, "none": KindSupervised, "supervised": KindSupervised,
		"SystemD": KindSystemd,
	} {
		got, err := ParseKind(in)
		if err != nil || got != want {
			t.Errorf("ParseKind(%q) = %q, %v; want %q", in, got, err, want)
		}
	}
	if _, err := ParseKind("upstart"); err == nil {
		t.Error("an unknown service kind must be rejected rather than silently ignored")
	}
}

func TestDetectKindMatchesHost(t *testing.T) {
	got := DetectKind()
	switch runtime.GOOS {
	case "darwin":
		if got != KindLaunchd {
			t.Fatalf("macOS should detect launchd, got %q", got)
		}
	case "linux":
		// Either is correct depending on whether the host or container has systemd as PID 1.
		if got != KindSystemd && got != KindSupervised {
			t.Fatalf("Linux should detect systemd or supervised, got %q", got)
		}
		if got == KindSystemd && !systemdIsInit() {
			t.Fatal("detected systemd without systemd as PID 1")
		}
	}
}

// Linger is the one behavior with no launchd counterpart: a systemd --user unit is torn down at
// logout unless it is set, so a user-mode install silently stops collecting. EnableLinger and
// LingerEnabled existed but had zero callers, which meant every user-mode systemd install
// inherited exactly that bug. These pin the wrapper that now invokes them.
func TestEnableLingerIfNeededOnlyAppliesToSystemdUserUnits(t *testing.T) {
	// Not applicable: nothing should be attempted or reported, so the caller records nothing and
	// no install prints a logout warning that does not apply to its backend.
	for _, m := range []Manager{
		{UserMode: false, Kind: KindSystemd}, // system units are not session-scoped
		{UserMode: true, Kind: KindLaunchd},  // gui/<uid> persists on its own
		{UserMode: true, Kind: KindSupervised},
		{UserMode: false, Kind: KindLaunchd},
	} {
		if got := m.EnableLingerIfNeeded(); got != (LingerOutcome{}) {
			t.Errorf("%+v should not attempt linger, got %+v", m, got)
		}
	}
}

// The applicable case must actually report something, or the manifest and doctor would have
// nothing to show and the gap would stay invisible.
func TestEnableLingerIfNeededReportsForSystemdUserUnits(t *testing.T) {
	got := (Manager{UserMode: true, Kind: KindSystemd}).EnableLingerIfNeeded()
	if !got.Applicable {
		t.Fatal("a systemd user unit must mark linger applicable")
	}
	if got.Detail == "" {
		t.Error("a systemd user unit must report a linger outcome, whether or not it succeeded")
	}
}

// The noun is shared by the install planner and the doctor fix planner. Duplicating it is how
// macOS-specific wording survived into the Linux paths in the first place.
func TestServiceNounNamesEachBackend(t *testing.T) {
	cases := map[Kind]string{
		KindLaunchd:    "launchd",
		KindSystemd:    "systemd",
		KindSupervised: "supervised",
	}
	for k, want := range cases {
		if got := k.ServiceNoun(); !strings.Contains(got, want) {
			t.Errorf("%s noun = %q, want it to mention %q", k, got, want)
		}
	}
	// An unknown kind must still describe something rather than returning empty.
	if Kind("weird").ServiceNoun() == "" {
		t.Error("an unrecognised kind must still yield a description")
	}
}

// The contract the install path depends on: every outcome says something. An outcome that reports
// nothing is indistinguishable from "linger does not apply here", which is how a successfully
// enabled linger used to go unrecorded in the manifest.
func TestEnableLingerAlwaysReportsADetail(t *testing.T) {
	// The guard cases, which never reach loginctl at all.
	if _, detail := EnableLinger(""); detail == "" {
		t.Error("an empty username is a reportable problem, not a silent skip")
	}
	if systemdIsInit() {
		return
	}
	if _, detail := EnableLinger("anyone"); detail == "" {
		t.Error("without systemd as PID 1, EnableLinger should still say why it did nothing")
	}
}

// systemd splits ExecStart on whitespace, so a path containing a space silently becomes two
// arguments and the unit fails with "No such file or directory". That is not an exotic input: a
// user-mode install lives under the user's home, and --collector takes an arbitrary path.
func TestSystemdUnitQuotesExecStartArguments(t *testing.T) {
	program := "/home/first last/.beacon/endpoint/beacon-otelcol"
	config := "/home/first last/.beacon/endpoint/otelcol.yaml"
	unit := unitFile(program, config, true)

	var execStart string
	for _, line := range strings.Split(unit, "\n") {
		if strings.HasPrefix(line, "ExecStart=") {
			execStart = line
		}
	}
	if execStart == "" {
		t.Fatal("unit has no ExecStart line")
	}
	// Each path must arrive as one quoted argument, not as bare text systemd would split.
	for _, want := range []string{`"` + program + `"`, `"` + config + `"`} {
		if !strings.Contains(execStart, want) {
			t.Errorf("ExecStart does not carry %s as a single quoted argument:\n%s", want, execStart)
		}
	}
}

// A `%` in a path is a unit specifier prefix anywhere on the line, quoted or not, so it has to be
// doubled or systemd expands it into something else entirely.
func TestSystemdArgEscapesSpecifiersAndQuotes(t *testing.T) {
	cases := map[string]string{
		"/opt/beacon/bin/beacon": `"/opt/beacon/bin/beacon"`,
		"/tmp/100% sure/beacon":  `"/tmp/100%% sure/beacon"`,
		`/tmp/quote"here/beacon`: `"/tmp/quote\"here/beacon"`,
		`/tmp/back\slash/beacon`: `"/tmp/back\\slash/beacon"`,
	}
	for in, want := range cases {
		if got := systemdArg(in); got != want {
			t.Errorf("systemdArg(%q) = %q, want %q", in, got, want)
		}
	}
}

// The updater unit is written by a separate function and had the same defect.
func TestUpdaterUnitQuotesExecStart(t *testing.T) {
	program := "/home/first last/bin/beacon"
	unit := updaterServiceUnit(program)
	if !strings.Contains(unit, `ExecStart="`+program+`" endpoint update --scheduled`) {
		t.Errorf("updater ExecStart does not quote the program path:\n%s", unit)
	}
}
