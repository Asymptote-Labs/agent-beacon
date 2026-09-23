package service

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/testenv"
)

// volumeFixture lays out a fake startup volume and a fake external volume under one temp dir and
// points the volume seams at them, so the detection and staging logic runs on any host. Paths under
// external report device 2; everything else reports device 1, the startup volume.
type volumeFixture struct {
	boot     string // stands in for the startup (Data) volume
	external string // stands in for /Volumes/<ExternalDisk>
	staging  string // the private staging directory Beacon should create on the startup volume
}

func newVolumeFixture(t *testing.T) volumeFixture {
	t.Helper()
	root := t.TempDir()
	f := volumeFixture{
		boot:     filepath.Join(root, "boot"),
		external: filepath.Join(root, "Volumes", "MacintoshExternalNVME"),
	}
	for _, dir := range []string{
		filepath.Join(f.boot, "Users", "test", "Library", "LaunchAgents"),
		filepath.Join(f.boot, "tmp"),
		filepath.Join(f.external, "Library", "LaunchAgents"),
	} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	f.staging = filepath.Join(f.boot, "tmp", launchdStagingDirName())

	oldID, oldRef, oldBases := launchdVolumeID, launchdBootVolumeRef, launchdStagingBases
	launchdBootVolumeRef = filepath.Join(f.boot, "Users")
	launchdStagingBases = func() []string { return []string{filepath.Join(f.boot, "tmp")} }
	launchdVolumeID = func(path string) (uint64, error) {
		if _, err := os.Stat(path); err != nil {
			return 0, err
		}
		if strings.HasPrefix(path, f.external) {
			return 2, nil
		}
		return 1, nil
	}
	t.Cleanup(func() { launchdVolumeID, launchdBootVolumeRef, launchdStagingBases = oldID, oldRef, oldBases })
	return f
}

// externalPlist writes a plist into the external volume's LaunchAgents, the layout from #639.
func (f volumeFixture) externalPlist(t *testing.T, label string) string {
	t.Helper()
	path := filepath.Join(f.external, "Library", "LaunchAgents", label+".plist")
	if err := os.WriteFile(path, []byte(plist(label, "/opt/homebrew/bin/beacon-otelcol", "/x/otelcol.yaml")), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func (f volumeFixture) bootPlist(t *testing.T, label string) string {
	t.Helper()
	path := filepath.Join(f.boot, "Users", "test", "Library", "LaunchAgents", label+".plist")
	if err := os.WriteFile(path, []byte(plist(label, "/opt/homebrew/bin/beacon-otelcol", "/x/otelcol.yaml")), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

// recordLaunchctl fakes launchctl with just enough state for the load paths: a bootstrap registers
// the plist's label (unless bootstrapOut makes it fail), a bootout drops it, and print reports
// whether the label is registered, with a pid so the forwarder's start wait is satisfied.
func recordLaunchctl(t *testing.T, bootstrapOut string) *[]string {
	t.Helper()
	calls := &[]string{}
	loaded := map[string]bool{}
	old := runLaunchctlCommand
	runLaunchctlCommand = func(args ...string) (string, error) {
		*calls = append(*calls, strings.Join(args, " "))
		switch args[0] {
		case "bootstrap":
			if bootstrapOut != "" {
				return bootstrapOut, errors.New("exit status 5")
			}
			loaded[strings.TrimSuffix(filepath.Base(args[len(args)-1]), ".plist")] = true
			return "", nil
		case "bootout":
			delete(loaded, filepath.Base(args[len(args)-1]))
			return "", nil
		case "print":
			if loaded[filepath.Base(args[len(args)-1])] {
				return "state = running\npid = 42\n", nil
			}
			return "Could not find service", errors.New("exit status 113")
		}
		return "", fmt.Errorf("unexpected launchctl call: %s", strings.Join(args, " "))
	}
	t.Cleanup(func() { runLaunchctlCommand = old })
	return calls
}

func bootstrapPaths(calls []string) []string {
	var out []string
	for _, c := range calls {
		if fields := strings.Fields(c); len(fields) == 3 && fields[0] == "bootstrap" {
			out = append(out, fields[2])
		}
	}
	return out
}

func TestInspectLaunchAgentVolumeFlagsPlistOffTheStartupVolume(t *testing.T) {
	f := newVolumeFixture(t)
	vol := InspectLaunchAgentVolume(f.externalPlist(t, UserLabel))
	if !vol.External {
		t.Fatalf("plist on the external volume was not flagged: %#v", vol)
	}
	if !testenv.HasPOSIXFileModes() {
		if vol.StagingDir != "" || vol.StagingError == "" {
			t.Fatalf("host without POSIX modes should reject private staging: %#v", vol)
		}
		return
	}
	if vol.StagingDir != f.staging {
		t.Fatalf("staging dir = %q, want %q on the startup volume", vol.StagingDir, f.staging)
	}
	if !strings.Contains(vol.Reason, "different volume") {
		t.Fatalf("reason should say why: %q", vol.Reason)
	}
}

func TestInspectLaunchAgentVolumeRejectsAnUnsafeStagingDirectory(t *testing.T) {
	testenv.RequirePOSIXFileModes(t)
	f := newVolumeFixture(t)
	if err := os.Mkdir(f.staging, 0o770); err != nil {
		t.Fatal(err)
	}

	vol := InspectLaunchAgentVolume(f.externalPlist(t, UserLabel))
	if !vol.External || vol.StagingDir != "" {
		t.Fatalf("unsafe staging directory reported usable: %#v", vol)
	}
	if !strings.Contains(vol.StagingError, "must not be accessible to other users") {
		t.Fatalf("staging error does not explain unsafe permissions: %#v", vol)
	}
}

func TestInspectLaunchAgentVolumeFallsBackFromAnUnsafeStagingDirectory(t *testing.T) {
	testenv.RequirePOSIXFileModes(t)
	f := newVolumeFixture(t)
	if err := os.Mkdir(f.staging, 0o770); err != nil {
		t.Fatal(err)
	}
	fallbackBase := filepath.Join(f.boot, "private-tmp")
	if err := os.Mkdir(fallbackBase, 0o777); err != nil {
		t.Fatal(err)
	}
	primaryBase := filepath.Dir(f.staging)
	oldBases := launchdStagingBases
	launchdStagingBases = func() []string { return []string{primaryBase, fallbackBase} }
	t.Cleanup(func() { launchdStagingBases = oldBases })

	vol := InspectLaunchAgentVolume(f.externalPlist(t, UserLabel))
	want := filepath.Join(fallbackBase, launchdStagingDirName())
	if vol.StagingDir != want || vol.StagingError != "" {
		t.Fatalf("fallback staging directory = %#v, want %s", vol, want)
	}
}

func TestInspectLaunchAgentVolumeLeavesStartupVolumeHomesAlone(t *testing.T) {
	f := newVolumeFixture(t)
	vol := InspectLaunchAgentVolume(f.bootPlist(t, UserLabel))
	if vol.External || vol.StagingDir != "" {
		t.Fatalf("a home on the startup volume must not be staged: %#v", vol)
	}
}

// When the volume lookup cannot run (a LaunchAgents directory that does not exist yet, or a stat the
// sandbox refuses), the path is the evidence left: external disks mount under /Volumes.
func TestInspectLaunchAgentVolumeFallsBackToTheVolumesPrefix(t *testing.T) {
	oldID := launchdVolumeID
	launchdVolumeID = func(string) (uint64, error) { return 0, errors.New("stat refused") }
	t.Cleanup(func() { launchdVolumeID = oldID })

	if vol := InspectLaunchAgentVolume("/Volumes/Ext/Library/LaunchAgents/" + UserLabel + ".plist"); !vol.External {
		t.Fatalf("/Volumes path not flagged without a volume lookup: %#v", vol)
	}
	if vol := InspectLaunchAgentVolume("/Users/test/Library/LaunchAgents/" + UserLabel + ".plist"); vol.External {
		t.Fatalf("/Users path flagged without evidence: %#v", vol)
	}
}

// The #639 reproduction: HOME on /Volumes/<disk>, launchd refuses the plist in place. Beacon must hand
// launchctl a byte-identical copy on the startup volume and never the external path.
func TestLoadLaunchdJobBootstrapsAStagedCopyForAnExternalHome(t *testing.T) {
	testenv.RequirePOSIXFileModes(t)
	f := newVolumeFixture(t)
	calls := recordLaunchctl(t, "")
	source := f.externalPlist(t, UserLabel)

	if err := loadLaunchdJob("gui/502", UserLabel, source); err != nil {
		t.Fatalf("loadLaunchdJob: %v", err)
	}
	staged := filepath.Join(f.staging, UserLabel+".plist")
	if got := bootstrapPaths(*calls); len(got) != 1 || got[0] != staged {
		t.Fatalf("bootstrap paths = %#v, want only the staged copy %s", got, staged)
	}
	for _, c := range *calls {
		if strings.Contains(c, f.external) {
			t.Fatalf("launchctl was handed the external path: %q", c)
		}
	}
	want, _ := os.ReadFile(source)
	got, err := os.ReadFile(staged)
	if err != nil || string(got) != string(want) {
		t.Fatalf("staged copy differs from the installed plist (err %v)", err)
	}
	if info, _ := os.Stat(f.staging); info == nil || info.Mode().Perm() != 0o700 {
		t.Fatalf("staging dir must be private (0700): %v", info)
	}
	// launchd refuses a job definition that is group- or world-writable.
	if info, _ := os.Stat(staged); info == nil || info.Mode().Perm() != 0o644 {
		t.Fatalf("staged plist mode = %v, want 0644", info)
	}
}

func TestLoadLaunchdJobBootstrapsInPlaceOnTheStartupVolume(t *testing.T) {
	f := newVolumeFixture(t)
	calls := recordLaunchctl(t, "")
	source := f.bootPlist(t, UserLabel)

	if err := loadLaunchdJob("gui/502", UserLabel, source); err != nil {
		t.Fatalf("loadLaunchdJob: %v", err)
	}
	if got := bootstrapPaths(*calls); len(got) != 1 || got[0] != source {
		t.Fatalf("bootstrap paths = %#v, want the plist in place", got)
	}
	if _, err := os.Stat(f.staging); !os.IsNotExist(err) {
		t.Fatalf("a startup-volume home must not create a staging dir (stat err %v)", err)
	}
}

// LaunchDaemons live in /Library on the startup volume and are root's; staging is a user-domain
// remedy only.
func TestLoadLaunchdJobNeverStagesTheSystemDomain(t *testing.T) {
	f := newVolumeFixture(t)
	calls := recordLaunchctl(t, "")
	source := f.externalPlist(t, SystemLabel)

	if err := loadLaunchdJob("system", SystemLabel, source); err != nil {
		t.Fatalf("loadLaunchdJob: %v", err)
	}
	if got := bootstrapPaths(*calls); len(got) != 1 || got[0] != source {
		t.Fatalf("bootstrap paths = %#v, want the plist in place for the system domain", got)
	}
}

// If launchd still refuses, the error names the external volume, the copy it tried, and the manual
// route, instead of the generic "verify the collector binary" advice alone.
func TestLoadLaunchdJobExplainsAnExternalHomeWhenBootstrapStillFails(t *testing.T) {
	f := newVolumeFixture(t)
	recordLaunchctl(t, "Bootstrap failed: 5: Input/output error")
	source := f.externalPlist(t, UserLabel)

	err := loadLaunchdJob("gui/502", UserLabel, source)
	if err == nil {
		t.Fatal("loadLaunchdJob succeeded, want the bootstrap failure")
	}
	wants := []string{
		"Bootstrap failed: 5",
		f.external,
		"not on the startup volume",
		"--no-start",
		"launchctl bootstrap gui/502",
	}
	if testenv.HasPOSIXFileModes() {
		// Staging succeeded, so the note names the copy launchd refused too.
		wants = append(wants, filepath.Join(f.staging, UserLabel+".plist"))
	} else {
		// No mode bits means the 0700 check cannot pass, so staging is refused and the note says
		// so; launchd never runs on such a host, but the explanation must still be coherent.
		wants = append(wants, "could not be staged")
	}
	for _, want := range wants {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("error missing %q:\n%v", want, err)
		}
	}
}

// The staging directory is a fixed name in a temp directory, so it must be one this user created
// and nobody else can write: a planted symlink or a writable directory would let another account
// swap the job definition between the copy and the bootstrap.
func TestLoadLaunchdJobRefusesAnUnsafeStagingDirectory(t *testing.T) {
	testenv.RequirePOSIXFileModes(t)
	for _, tc := range []struct {
		name  string
		plant func(t *testing.T, staging, elsewhere string)
	}{
		{"symlink", func(t *testing.T, staging, elsewhere string) {
			if err := os.Symlink(elsewhere, staging); err != nil {
				t.Fatal(err)
			}
		}},
		{"group writable", func(t *testing.T, staging, _ string) {
			if err := os.Mkdir(staging, 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.Chmod(staging, 0o770); err != nil {
				t.Fatal(err)
			}
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newVolumeFixture(t)
			elsewhere := t.TempDir()
			tc.plant(t, f.staging, elsewhere)
			calls := recordLaunchctl(t, "Bootstrap failed: 5: Input/output error")
			source := f.externalPlist(t, UserLabel)

			err := loadLaunchdJob("gui/502", UserLabel, source)
			if err == nil || !strings.Contains(err.Error(), "could not be staged") {
				t.Fatalf("error = %v, want it to say the copy could not be staged", err)
			}
			if got := bootstrapPaths(*calls); len(got) != 1 || got[0] != source {
				t.Fatalf("bootstrap paths = %#v, want the original plist when staging is refused", got)
			}
			if entries, _ := os.ReadDir(elsewhere); len(entries) != 0 {
				t.Fatalf("staging wrote through the planted directory: %v", entries)
			}
		})
	}
}

// A planted symlink at the staged file's own name is replaced, not followed.
func TestStageLaunchdPlistReplacesAPlantedSymlink(t *testing.T) {
	testenv.RequirePOSIXFileModes(t)
	f := newVolumeFixture(t)
	source := f.externalPlist(t, UserLabel)
	if err := os.Mkdir(f.staging, 0o700); err != nil {
		t.Fatal(err)
	}
	victim := filepath.Join(t.TempDir(), "victim")
	if err := os.WriteFile(victim, []byte("untouched"), 0o600); err != nil {
		t.Fatal(err)
	}
	dest := filepath.Join(f.staging, UserLabel+".plist")
	if err := os.Symlink(victim, dest); err != nil {
		t.Fatal(err)
	}

	staged, err := stageLaunchdPlist(source, f.staging)
	if err != nil || staged != dest {
		t.Fatalf("stageLaunchdPlist = %q, %v", staged, err)
	}
	if got, _ := os.ReadFile(victim); string(got) != "untouched" {
		t.Fatalf("staging followed the symlink and overwrote %s", victim)
	}
	if info, err := os.Lstat(dest); err != nil || info.Mode()&os.ModeSymlink != 0 {
		t.Fatalf("staged plist should be a regular file: %v %v", info, err)
	}
}

func TestBootoutLaunchdJobRemovesTheStagedCopy(t *testing.T) {
	testenv.RequirePOSIXFileModes(t)
	f := newVolumeFixture(t)
	calls := recordLaunchctl(t, "")
	if err := loadLaunchdJob("gui/502", InventoryLabel, f.externalPlist(t, InventoryLabel)); err != nil {
		t.Fatal(err)
	}
	staged := filepath.Join(f.staging, InventoryLabel+".plist")
	if _, err := os.Stat(staged); err != nil {
		t.Fatalf("staged copy missing after load: %v", err)
	}

	if err := bootoutLaunchdJob("gui/502", InventoryLabel); err != nil {
		t.Fatalf("bootoutLaunchdJob: %v", err)
	}
	if last := (*calls)[len(*calls)-1]; last != "bootout gui/502/"+InventoryLabel {
		t.Fatalf("last launchctl call = %q, want bootout by label", last)
	}
	if _, err := os.Stat(staged); !os.IsNotExist(err) {
		t.Fatalf("staged copy survived bootout (stat err %v)", err)
	}
}

func TestVolumeIDAgreesWithinOneFilesystem(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("launchd volume detection is not built for Windows")
	}
	dir := t.TempDir()
	sub := filepath.Join(dir, "sub")
	if err := os.Mkdir(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	a, errA := volumeID(dir)
	b, errB := volumeID(sub)
	if errA != nil || errB != nil || a != b {
		t.Fatalf("volumeID(%s)=%d,%v volumeID(%s)=%d,%v; want equal", dir, a, errA, sub, b, errB)
	}
	if _, err := volumeID(filepath.Join(dir, "missing")); err == nil {
		t.Fatal("volumeID of a missing path should fail")
	}
}

// End to end through each user LaunchAgent the endpoint installs: collector, forwarder and the
// inventory job all reached launchd from the external volume in #639. The managers refuse to run
// off macOS, so this half runs on the macOS CI job; the shared path is covered above everywhere.
func TestUserLaunchAgentsBootstrapFromAStagedCopyOnAnExternalHome(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("the launchd managers only run on macOS; loadLaunchdJob covers the shared path elsewhere")
	}
	shrinkLaunchdWaits(t)
	f := newVolumeFixture(t)
	testenv.SetHome(t, f.external)
	calls := recordLaunchctl(t, "")

	collector := Manager{UserMode: true, Kind: KindLaunchd}
	if _, err := collector.WriteUnit("/opt/homebrew/bin/beacon-otelcol", "/x/otelcol.yaml"); err != nil {
		t.Fatal(err)
	}
	if err := collector.Load(); err != nil {
		t.Fatalf("collector Load: %v", err)
	}
	forwarder := ForwarderManager{UserMode: true, Kind: KindLaunchd}
	if _, err := forwarder.WriteUnit("/opt/beacon/bin/vector", "/x/vector.toml"); err != nil {
		t.Fatal(err)
	}
	if err := forwarder.Load(); err != nil {
		t.Fatalf("forwarder Load: %v", err)
	}
	inventory := InventoryManager{UserMode: true, Kind: KindLaunchd}
	if _, err := inventory.WriteUnit("/opt/homebrew/bin/beacon", ""); err != nil {
		t.Fatal(err)
	}
	if err := inventory.Load(); err != nil {
		t.Fatalf("inventory Load: %v", err)
	}
	for _, p := range bootstrapPaths(*calls) {
		if !strings.HasPrefix(p, f.staging+string(filepath.Separator)) {
			t.Fatalf("bootstrap from %s, want a staged copy under %s", p, f.staging)
		}
	}
	for _, label := range []string{UserLabel, ForwarderLabel, InventoryLabel} {
		if _, err := os.Stat(filepath.Join(f.staging, label+".plist")); err != nil {
			t.Fatalf("no staged copy for %s: %v", label, err)
		}
	}
}
