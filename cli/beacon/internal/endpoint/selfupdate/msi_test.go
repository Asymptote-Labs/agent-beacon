package selfupdate

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// exitStatus stands in for an *exec.ExitError carrying code.
type exitStatus int

func (e exitStatus) Error() string { return fmt.Sprintf("exit status %d", int(e)) }
func (e exitStatus) ExitCode() int { return int(e) }

const (
	enterpriseMSI = "beacon-enterprise_1.3.41_windows_amd64.msi"
	publicMSI     = "BeaconEndpointAgent-1.3.40-x64.msi"
)

// msiFixture is a Windows install of the public edition under a temporary Program Files, with a
// signed release serving the enterprise MSI and the public one, and an msiexec that records what it
// was asked to do and installs whatever beacon.exe the package would.
type msiFixture struct {
	a          *Applier
	enterprise Package
	public     Package
	// installs maps a package's staged file name prefix to the version line its beacon.exe reports.
	reports map[string]string
	// installErr, keyed by "/i" or "/x" and the step, is what msiexec exits with.
	fail  map[string]error
	calls []string
}

func newMSIFixture(t *testing.T) *msiFixture {
	t.Helper()
	key := throwawayKey(t, "release")
	pinKeys(t, keyFingerprint(key))
	enterpriseBytes, publicBytes := []byte("enterprise msi"), []byte("public msi")
	srv := signedReleaseServer(t, key, map[string][]byte{enterpriseMSI: enterpriseBytes, publicMSI: publicBytes})
	checksums := SignedChecksums{URL: srv.URL + "/checksums.txt", SignatureURL: srv.URL + "/checksums.txt.sig", KeyURL: srv.URL + "/" + releaseKeyAsset}

	f := &msiFixture{
		enterprise: Package{URL: srv.URL + "/" + enterpriseMSI, SHA256: sha256Hex(enterpriseBytes), Version: "1.3.41", Edition: "enterprise", Name: enterpriseMSI, Checksums: checksums},
		public:     Package{URL: srv.URL + "/" + publicMSI, Version: "1.3.40", Name: publicMSI, Checksums: checksums},
		reports: map[string]string{
			"download":         "beacon version 1.3.41 (abc) built on today, enterprise edition",
			"rollback-package": "beacon version 1.3.40 (abc) built on today",
		},
		fail: map[string]error{},
	}
	a := NewApplier("1.3.40")
	a.StageDir = t.TempDir()
	a.LogPath = filepath.Join(t.TempDir(), "system.jsonl")
	a.windowsDir = filepath.Join(t.TempDir(), "Program Files", "Beacon")
	a.executable = func() (string, error) { return "/somewhere/else/beacon", nil }
	a.collectorRunning = func() bool { return true }
	bin := a.installedWindowsBeacon()
	if err := os.MkdirAll(filepath.Dir(bin), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(bin, []byte("beacon version 1.3.40 (abc) built on today"), 0o755); err != nil {
		t.Fatal(err)
	}
	a.run = func(_ context.Context, name string, args ...string) (string, error) {
		if name == bin {
			data, err := os.ReadFile(bin)
			return string(data), err
		}
		if !strings.Contains(name, "msiexec") {
			return "", errors.New("ran " + name)
		}
		if len(args) != 6 || args[2] != "/qn" || args[3] != "/norestart" || args[4] != "/l*v" || !strings.HasPrefix(filepath.Base(args[5]), "msiexec-") {
			return "", errors.New("unexpected msiexec arguments: " + strings.Join(args, " "))
		}
		op, pkg := args[0], filepath.Base(args[1])
		f.calls = append(f.calls, op+" "+pkg)
		failure := f.fail[op+" "+pkg]
		if failure != nil && exitCode(failure) != msiSuccessRebootRequired {
			return "", failure
		}
		if op == "/i" {
			for prefix, line := range f.reports {
				if strings.HasPrefix(pkg, prefix) {
					if err := os.WriteFile(bin, []byte(line), 0o755); err != nil {
						return "", err
					}
				}
			}
		}
		return "", failure
	}
	f.a = a
	return f
}

func (f *msiFixture) installed(t *testing.T) string {
	t.Helper()
	return string(mustRead(t, f.a.installedWindowsBeacon()))
}

func TestInstallPackageInstallsAnMSIWithMsiexec(t *testing.T) {
	f := newMSIFixture(t)
	f.fail["/i download.msi"] = exitStatus(msiSuccessRebootRequired)
	res, err := f.a.InstallPackage(context.Background(), f.enterprise)
	if err != nil {
		t.Fatalf("InstallPackage: %v", err)
	}
	if !res.Applied || res.RolledBack || res.Verification != "sha256+gpg" {
		t.Fatalf("result = %+v", res)
	}
	if !strings.Contains(res.Message, "next restart") {
		t.Fatalf("message = %q, want the pending restart named", res.Message)
	}
	if !slices.Equal(f.calls, []string{"/i download.msi"}) {
		t.Fatalf("msiexec calls = %v", f.calls)
	}
	if !strings.Contains(f.installed(t), "enterprise edition") {
		t.Fatalf("installed = %q", f.installed(t))
	}
	// No snapshot of the install tree: Windows Installer owns those files.
	if _, err := os.Stat(filepath.Join(f.a.StageDir, "rollback")); !os.IsNotExist(err) {
		t.Fatalf("a snapshot was taken (%v)", err)
	}
}

// A package checksums.txt does not list is refused before msiexec runs, whatever SHA-256 it was
// offered with.
func TestInstallPackageRefusesAnMSIThatIsNotSigned(t *testing.T) {
	f := newMSIFixture(t)
	f.enterprise.Name = "beacon-enterprise_9.9.9_windows_amd64.msi"
	_, err := f.a.InstallPackage(context.Background(), f.enterprise)
	if err == nil || !strings.Contains(err.Error(), "verify release signature") {
		t.Fatalf("err = %v", err)
	}
	if len(f.calls) != 0 {
		t.Fatalf("msiexec ran: %v", f.calls)
	}

	f = newMSIFixture(t)
	pinKeys(t, keyFingerprint(throwawayKey(t, "not the release key")))
	if _, err := f.a.InstallPackage(context.Background(), f.enterprise); err == nil || len(f.calls) != 0 {
		t.Fatalf("err = %v, calls %v", err, f.calls)
	}
}

// A package that installs the wrong edition is removed and the previous MSI reinstalled.
func TestInstallPackageRollsAnMSIBackByReinstallingThePreviousOne(t *testing.T) {
	f := newMSIFixture(t)
	f.reports["download"] = "beacon version 1.3.41 (abc) built on today"
	f.a.RollbackPackage = &f.public
	res, err := f.a.InstallPackage(context.Background(), f.enterprise)
	if err == nil || !strings.Contains(err.Error(), "expected the enterprise edition") {
		t.Fatalf("err = %v", err)
	}
	if !res.RolledBack || res.Applied {
		t.Fatalf("result = %+v", res)
	}
	want := []string{"/i download.msi", "/x download.msi", "/i rollback-package.msi"}
	if !slices.Equal(f.calls, want) {
		t.Fatalf("msiexec calls = %v, want %v", f.calls, want)
	}
	if got := f.installed(t); got != "beacon version 1.3.40 (abc) built on today" {
		t.Fatalf("installed after rollback = %q", got)
	}
}

// When msiexec itself fails, the failed package may not be installed at all (1605 on removal), and
// the previous one is reinstalled all the same.
func TestInstallPackageReinstallsThePreviousMSIAfterMsiexecFails(t *testing.T) {
	f := newMSIFixture(t)
	f.a.RollbackPackage = &f.public
	f.fail["/i download.msi"] = exitStatus(1603)
	f.fail["/x download.msi"] = exitStatus(msiUnknownProduct)
	res, err := f.a.InstallPackage(context.Background(), f.enterprise)
	if err == nil || !strings.Contains(err.Error(), "exited with 1603") || !strings.Contains(err.Error(), "msiexec-install.log") {
		t.Fatalf("err = %v, want msiexec's exit code and log", err)
	}
	if !res.RolledBack {
		t.Fatalf("result = %+v", res)
	}
	if diag := string(mustRead(t, filepath.Join(f.a.StageDir, "last_failure.json"))); strings.Contains(diag, "rollback_error") {
		t.Fatalf("a rollback that succeeded was recorded as failed:\n%s", diag)
	}
}

// Without the previous package there is nothing to reinstall, which is said rather than claimed.
func TestInstallPackageSaysAnMSIWithoutAPreviousPackageWasNotRolledBack(t *testing.T) {
	f := newMSIFixture(t)
	f.reports["download"] = "beacon version 1.3.41 (abc) built on today"
	res, err := f.a.InstallPackage(context.Background(), f.enterprise)
	if err == nil || res.RolledBack {
		t.Fatalf("err = %v, result = %+v", err, res)
	}
	diag := string(mustRead(t, filepath.Join(f.a.StageDir, "last_failure.json")))
	if !strings.Contains(diag, "no previous package to reinstall") {
		t.Fatalf("diagnostic = %s", diag)
	}
	if !slices.Equal(f.calls, []string{"/i download.msi"}) {
		t.Fatalf("msiexec calls = %v", f.calls)
	}
}

// The previous package is fetched and verified before anything is installed.
func TestInstallPackageRefusesAnMSIWhenThePreviousOneDoesNotVerify(t *testing.T) {
	f := newMSIFixture(t)
	back := f.public
	back.Name = "BeaconEndpointAgent-1.3.39-x64.msi"
	f.a.RollbackPackage = &back
	if _, err := f.a.InstallPackage(context.Background(), f.enterprise); err == nil || len(f.calls) != 0 {
		t.Fatalf("err = %v, calls %v", err, f.calls)
	}
}

// Windows will not replace a running executable, so a beacon.exe that is running this install is
// moved out of the way first, and put back when the rollback leaves none. An unlocked copy stays at
// the installed path through msiexec, because the old product's uninstall script runs it to stop the
// services and does nothing when it is missing.
func TestInstallPackageMovesTheRunningBeaconAside(t *testing.T) {
	f := newMSIFixture(t)
	bin := f.a.installedWindowsBeacon()
	f.a.executable = func() (string, error) { return bin, nil }
	run := f.a.run
	var atInstall string
	f.a.run = func(ctx context.Context, name string, args ...string) (string, error) {
		if len(args) > 0 && args[0] == "/i" && atInstall == "" {
			data, err := os.ReadFile(bin)
			atInstall = string(data)
			if err != nil {
				atInstall = "missing: " + err.Error()
			}
		}
		return run(ctx, name, args...)
	}
	if _, err := f.a.InstallPackage(context.Background(), f.enterprise); err != nil {
		t.Fatalf("InstallPackage: %v", err)
	}
	if !strings.Contains(f.installed(t), "enterprise edition") {
		t.Fatalf("installed = %q", f.installed(t))
	}
	if atInstall != "beacon version 1.3.40 (abc) built on today" {
		t.Fatalf("beacon.exe when msiexec ran = %q, want a copy of the one moved aside", atInstall)
	}

	// msiexec fails before laying any file down and there is no previous package: the running
	// binary goes back where it was.
	f = newMSIFixture(t)
	bin = f.a.installedWindowsBeacon()
	f.a.executable = func() (string, error) { return bin, nil }
	f.fail["/i download.msi"] = exitStatus(1618)
	if _, err := f.a.InstallPackage(context.Background(), f.enterprise); err == nil {
		t.Fatal("installed")
	}
	if got := f.installed(t); got != "beacon version 1.3.40 (abc) built on today" {
		t.Fatalf("installed = %q, want the binary that was moved aside", got)
	}
}

func TestMsiexecIsWindowsInstallersOwn(t *testing.T) {
	t.Setenv("SystemRoot", filepath.Join("C:", "Windows"))
	if got := msiexecPath(); got != filepath.Join("C:", "Windows", "System32", "msiexec.exe") {
		t.Fatalf("msiexecPath = %q", got)
	}
}
