package service

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestManagerLabelAndPlistPath(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	userManager := Manager{UserMode: true}
	if got := userManager.Label(); got != UserLabel {
		t.Fatalf("user label = %q, want %q", got, UserLabel)
	}
	userPath, err := userManager.PlistPath()
	if err != nil {
		t.Fatalf("user PlistPath returned error: %v", err)
	}
	if want := filepath.Join(home, "Library", "LaunchAgents", UserLabel+".plist"); userPath != want {
		t.Fatalf("user plist path = %q, want %q", userPath, want)
	}

	systemManager := Manager{}
	if got := systemManager.Label(); got != SystemLabel {
		t.Fatalf("system label = %q, want %q", got, SystemLabel)
	}
	systemPath, err := systemManager.PlistPath()
	if err != nil {
		t.Fatalf("system PlistPath returned error: %v", err)
	}
	if want := filepath.Join("/Library/LaunchDaemons", SystemLabel+".plist"); systemPath != want {
		t.Fatalf("system plist path = %q, want %q", systemPath, want)
	}
}

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

func TestWritePlistUserMode(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	manager := Manager{UserMode: true}

	path, err := manager.WritePlist("/tmp/otelcol", "/tmp/otelcol.yaml")
	if runtime.GOOS != "darwin" {
		if err == nil || !strings.Contains(err.Error(), "supported only on macOS") {
			t.Fatalf("WritePlist non-darwin error = %v, want macOS support error", err)
		}
		return
	}
	if err != nil {
		t.Fatalf("WritePlist returned error: %v", err)
	}
	if got, want := path, filepath.Join(home, "Library", "LaunchAgents", UserLabel+".plist"); got != want {
		t.Fatalf("plist path = %q, want %q", got, want)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read plist: %v", err)
	}
	if !strings.Contains(string(data), "<string>/tmp/otelcol</string>") {
		t.Fatalf("plist content missing program: %s", string(data))
	}
}

func TestStatusNonDarwinDocumentsUnsupportedMessage(t *testing.T) {
	if runtime.GOOS == "darwin" {
		t.Skip("non-darwin status contract")
	}
	status := (Manager{UserMode: true}).Status()
	if status.Label != UserLabel {
		t.Fatalf("Label = %q, want %q", status.Label, UserLabel)
	}
	if status.Loaded || status.Running {
		t.Fatalf("non-darwin status should not be loaded/running: %#v", status)
	}
	if !strings.Contains(status.Message, "available only on macOS") {
		t.Fatalf("unexpected status message: %q", status.Message)
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

func TestLoadAndUnloadAreNoOpsOffDarwin(t *testing.T) {
	if runtime.GOOS == "darwin" {
		t.Skip("non-darwin launchd contract")
	}
	manager := Manager{UserMode: true}
	called := false
	oldRun := runLaunchctlCommand
	runLaunchctlCommand = func(args ...string) (string, error) {
		called = true
		return "", errors.New("launchctl should not run off darwin")
	}
	t.Cleanup(func() {
		runLaunchctlCommand = oldRun
	})

	if err := manager.Load(); err != nil {
		t.Fatalf("Load off darwin = %v, want nil", err)
	}
	if err := manager.Unload(); err != nil {
		t.Fatalf("Unload off darwin = %v, want nil", err)
	}
	if called {
		t.Fatal("launchctl was invoked off darwin")
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
