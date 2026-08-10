package hostguard

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// A guard that cannot detect a change is worse than no guard: it converts "we did not look"
// into "we verified the host was untouched". But a guard that fires on every run is equally
// useless, because it gets ignored. These tests pin both edges.

func snap(entries ...guarded) Snapshot {
	s := Snapshot{ServicesAvailable: true, Paths: map[string]string{}}
	for _, g := range entries {
		s.Paths[g.path] = digest(g)
	}
	return s
}

func TestConfigEditIsDetected(t *testing.T) {
	f := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(f, []byte(`{"log_path":"/a"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	g := guarded{f, watchFull}

	before := snap(g)
	if err := os.WriteFile(f, []byte(`{"log_path":"/b"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if Compare(before, snap(g)).Clean() {
		t.Fatal("an edited config file must be detected: that is a local install")
	}
}

func TestConfigCreationIsDetectedAndLabelled(t *testing.T) {
	f := filepath.Join(t.TempDir(), "config.json")
	g := guarded{f, watchFull}

	before := snap(g)
	if err := os.WriteFile(f, []byte(`{}`), 0o600); err != nil {
		t.Fatal(err)
	}
	d := Compare(before, snap(g))
	if d.Clean() {
		t.Fatal("creating a guarded config must be detected")
	}
	if !strings.Contains(d.Describe(), "absent -> present") {
		t.Errorf("the absent->present transition should be explicit, got %q", d.Describe())
	}
}

// The case that made the first version of this guard unusable: the developer's own Beacon
// collector appends to its log continuously, so log growth must NOT be a failure.
func TestLogGrowthIsIgnored(t *testing.T) {
	dir := t.TempDir()
	logDir := filepath.Join(dir, "logs")
	if err := os.MkdirAll(logDir, 0o755); err != nil {
		t.Fatal(err)
	}
	logFile := filepath.Join(logDir, "runtime.jsonl")
	if err := os.WriteFile(logFile, []byte("{\"a\":1}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	g := guarded{logDir, watchExistence}

	before := snap(g)
	// Simulate the real collector appending, and a rotated archive appearing.
	f, err := os.OpenFile(logFile, os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	f.WriteString(strings.Repeat("{\"b\":2}\n", 500))
	f.Close()
	if err := os.WriteFile(filepath.Join(logDir, "runtime.jsonl.1"), []byte("old\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	if d := Compare(before, snap(g)); !d.Clean() {
		t.Fatalf("log growth is the developer's own agent working normally and must not fire: %s",
			d.Describe())
	}
}

// ...but the log directory being *created* is a signal, since that means something installed.
func TestLogDirCreationIsDetected(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "beacon-agent")
	g := guarded{dir, watchExistence}

	before := snap(g)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if Compare(before, snap(g)).Clean() {
		t.Fatal("creating a guarded install/log root must be detected")
	}
}

func TestExistenceModeChangeIsDetected(t *testing.T) {
	// os.Chmod on Windows only toggles the read-only attribute; there are no permission bits to
	// change, so the signal this asserts cannot be produced there. The existence half of
	// watchExistence is covered by the other tests on every platform.
	if runtime.GOOS == "windows" {
		t.Skip("file mode bits are a POSIX concept")
	}
	dir := t.TempDir()
	sub := filepath.Join(dir, "opt-beacon")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	g := guarded{sub, watchExistence}

	before := snap(g)
	if err := os.Chmod(sub, 0o700); err != nil {
		t.Fatal(err)
	}
	if Compare(before, snap(g)).Clean() {
		t.Fatal("a permission change on a guarded root must be detected")
	}
}

// Unit/plist directories: a new Beacon service file is the clearest escape signal, while
// unrelated services churning must be ignored.
func TestNewBeaconUnitIsDetectedAndUnrelatedIsIgnored(t *testing.T) {
	dir := t.TempDir()
	g := guarded{dir, watchBeaconEntries}

	if err := os.WriteFile(filepath.Join(dir, "com.example.other.plist"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	before := snap(g)

	// An unrelated service appearing must not fire.
	if err := os.WriteFile(filepath.Join(dir, "com.vendor.unrelated.plist"), []byte("y"), 0o644); err != nil {
		t.Fatal(err)
	}
	if d := Compare(before, snap(g)); !d.Clean() {
		t.Fatalf("unrelated services must be ignored, got %s", d.Describe())
	}

	// A Beacon service appearing must fire.
	if err := os.WriteFile(filepath.Join(dir, "com.beacon.endpoint.collector.plist"), []byte("z"), 0o644); err != nil {
		t.Fatal(err)
	}
	d := Compare(before, snap(g))
	if d.Clean() {
		t.Fatal("a new Beacon unit/plist must be detected")
	}
	if !strings.Contains(d.Describe(), "com.beacon.endpoint.collector.plist") {
		t.Errorf("the diff should name the new unit, got %q", d.Describe())
	}
}

func TestServiceAppearanceIsDetected(t *testing.T) {
	before := Snapshot{ServicesAvailable: true, Paths: map[string]string{}}
	after := Snapshot{ServicesAvailable: true, Paths: map[string]string{}, Services: []string{"com.beacon.endpoint.collector"}}

	d := Compare(before, after)
	if d.Clean() {
		t.Fatal("a newly registered Beacon service must be detected")
	}
	if len(d.NewServices) != 1 {
		t.Errorf("expected the service named, got %v", d.NewServices)
	}
}

// A running collector's pid changes on restart; that must not read as a state change, or the
// guard fires whenever the developer's own service restarts.
func TestServicePidChurnIsIgnored(t *testing.T) {
	before := Snapshot{ServicesAvailable: true, Paths: map[string]string{}, Services: []string{"com.beacon.endpoint.collector"}}
	after := Snapshot{ServicesAvailable: true, Paths: map[string]string{}, Services: []string{"com.beacon.endpoint.collector"}}
	if !Compare(before, after).Clean() {
		t.Error("the same service present in both snapshots must be clean")
	}
}

func TestUnchangedStateIsClean(t *testing.T) {
	dir := t.TempDir()
	f := filepath.Join(dir, "config.json")
	if err := os.WriteFile(f, []byte(`{}`), 0o600); err != nil {
		t.Fatal(err)
	}
	entries := []guarded{{f, watchFull}, {dir, watchExistence}, {dir, watchBeaconEntries}}
	if d := Compare(snap(entries...), snap(entries...)); !d.Clean() {
		t.Fatalf("expected clean, got %s", d.Describe())
	}
}

// The guarded set must actually cover what a Beacon install touches, or the guard looks in the
// wrong place and passes vacuously.
func TestGuardedPathsCoverBeaconInstallSurface(t *testing.T) {
	// Compared with forward slashes, because the guarded set is built with filepath.Join and
	// therefore renders with the host separator. The assertion is about which paths are guarded,
	// not how they are spelled -- on Windows the backslash form made every one of these look
	// uncovered while the guard was in fact watching exactly the right places.
	slashed := make([]string, 0, len(GuardedPaths()))
	for _, p := range GuardedPaths() {
		slashed = append(slashed, filepath.ToSlash(p))
	}
	joined := strings.Join(slashed, "\n")
	for _, want := range []string{
		".beacon/endpoint/config.json",
		".beacon/endpoint/otelcol.yaml",
		".claude/settings.json",
		"/opt/beacon",
		"/var/log/beacon-agent",
	} {
		if !strings.Contains(joined, want) {
			t.Errorf("guarded paths should cover %q, got:\n%s", want, joined)
		}
	}
}

// Guard against a regression where a real install root gets watched with a content digest
// again, which is what made this unusable on a machine running Beacon.
func TestLogAndInstallRootsAreExistenceOnly(t *testing.T) {
	for _, g := range guardedPaths() {
		base := filepath.Base(g.path)
		if base == "beacon-agent" || base == "logs" || g.path == "/opt/beacon" {
			if g.mode != watchExistence {
				t.Errorf("%s must be watched existence-only, or the developer's own "+
					"collector trips the guard on every run", g.path)
			}
		}
	}
}

// An unavailable service probe used to be encoded as a fake service name, which Compare then
// treated as a real service appearing or disappearing. On a machine that already runs Beacon
// services, a flaky launchctl or systemctl therefore produced a host-escape finding manufactured by
// the guard itself -- on the most serious check this tool has. Cursor Bugbot reported it.
func TestUnavailableServiceProbeIsNotAnEscape(t *testing.T) {
	// The developer's machine already runs Beacon services; the probe fails on the second pass.
	before := Snapshot{ServicesAvailable: true, Paths: map[string]string{},
		Services: []string{"com.beacon.endpoint.collector"}}
	after := Snapshot{ServicesAvailable: false, Paths: map[string]string{}}

	d := Compare(before, after)

	if len(d.GoneServices) != 0 {
		t.Errorf("an unavailable probe must not report services as gone, got %v", d.GoneServices)
	}
	if !d.ServicesUnverified {
		t.Error("the diff should record that services were not verified")
	}
	if !d.Clean() {
		t.Errorf("a failed probe is not evidence of a change, so it must not read as dirty: %q",
			d.Describe())
	}
	// But it must not claim a clean bill of health either.
	if d.Describe() == CleanDescription {
		t.Errorf("an unverified service half must not describe itself as fully unchanged: %q",
			d.Describe())
	}
	if d.Describe() != PartialDescription {
		t.Errorf("Describe = %q, want the partial description", d.Describe())
	}
}

// The reverse direction too: a probe that failed first and succeeded later.
func TestUnavailableBeforeProbeIsAlsoNotAnEscape(t *testing.T) {
	before := Snapshot{ServicesAvailable: false, Paths: map[string]string{}}
	after := Snapshot{ServicesAvailable: true, Paths: map[string]string{},
		Services: []string{"com.beacon.endpoint.collector"}}

	d := Compare(before, after)

	if len(d.NewServices) != 0 {
		t.Errorf("a service that may have existed all along must not read as new, got %v", d.NewServices)
	}
	if !d.ServicesUnverified {
		t.Error("the diff should record that services were not verified")
	}
}

// A real file change still fails even when the service probe was unavailable -- the file half of
// the guard is independent and must keep working.
func TestFileChangeStillDetectedWhenServiceProbeFails(t *testing.T) {
	before := Snapshot{ServicesAvailable: false, Paths: map[string]string{"/x": "aaa"}}
	after := Snapshot{ServicesAvailable: false, Paths: map[string]string{"/x": "bbb"}}

	d := Compare(before, after)

	if d.Clean() {
		t.Fatal("a modified guarded path must still be detected")
	}
	if d.Describe() == PartialDescription {
		t.Errorf("a real change must be described, not reported as merely unverified: %q", d.Describe())
	}
}
