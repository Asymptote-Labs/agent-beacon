// Package hostguard proves the harness did not touch the developer's own Beacon state.
//
// Every Beacon install and service operation is supposed to happen inside a sandbox. That is
// worth enforcing rather than asserting: a stray code path running `beacon endpoint install`
// locally would clobber a real ~/.beacon, real launchd jobs, and a real runtime log.
//
// The hard part is precision. A developer working on Beacon usually has Beacon *installed and
// running*, so its collector and hooks are continuously appending to `~/.beacon` and
// `/var/log/beacon-agent`. A guard that hashes those trees fires on every run and is therefore
// useless as a gate. So paths are watched by intent:
//
//   - structural: config, collector YAML, install manifests, per-runtime settings files. Full
//     content digest. A change here is a harness escape.
//   - existence: install roots and log directories. Existence and mode only. Their *creation*
//     is a signal; their growth is the developer's own agent working normally.
//   - services: launchd/systemd entries whose name mentions beacon, by name. A newly
//     registered service is the clearest escape signal there is.
package hostguard

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
)

// watchMode selects how much of a path is fingerprinted.
type watchMode int

const (
	// watchFull digests file contents. For config that must not change.
	watchFull watchMode = iota
	// watchExistence records only presence and mode, so a growing log does not trip it.
	watchExistence
	// watchBeaconEntries lists directory entries whose name mentions beacon, ignoring size.
	// For unit/plist directories, where a new Beacon service appearing is the signal.
	watchBeaconEntries
)

type guarded struct {
	path string
	mode watchMode
}

// Snapshot is a fingerprint of the guarded host state at one point in time.
type Snapshot struct {
	Paths map[string]string
	// Services lists Beacon-related service names visible to this user.
	Services []string
	// ServicesAvailable is false when the service probe could not run, in which case Services
	// says nothing. Kept separate so an unavailable probe is never compared as if it had
	// returned an empty list -- that would read as every service having disappeared.
	ServicesAvailable bool
}

func home() string {
	h, err := os.UserHomeDir()
	if err != nil {
		return os.Getenv("HOME")
	}
	return h
}

// guardedPaths returns what to watch and how.
func guardedPaths() []guarded {
	h := home()
	out := []guarded{
		// Structural: if any of these change, something configured Beacon locally.
		{filepath.Join(h, ".beacon", "endpoint", "config.json"), watchFull},
		{filepath.Join(h, ".beacon", "endpoint", "otelcol.yaml"), watchFull},
		{filepath.Join(h, ".beacon", "endpoint", "install-manifest.json"), watchFull},
		{filepath.Join(h, ".claude", "settings.json"), watchFull},
		{filepath.Join(h, ".codex", "config.toml"), watchFull},
		{filepath.Join(h, ".cursor", "hooks.json"), watchFull},

		// Existence only: these legitimately grow when the developer runs Beacon.
		{filepath.Join(h, ".beacon"), watchExistence},
		{filepath.Join(h, ".beacon", "endpoint", "logs"), watchExistence},
		{"/opt/beacon", watchExistence},
		{"/var/log/beacon-agent", watchExistence},
	}
	switch runtime.GOOS {
	case "darwin":
		out = append(out,
			guarded{"/Library/Application Support/Beacon/Endpoint/config.json", watchFull},
			guarded{filepath.Join(h, "Library", "LaunchAgents"), watchBeaconEntries},
			guarded{"/Library/LaunchDaemons", watchBeaconEntries},
		)
	case "linux":
		out = append(out,
			guarded{"/etc/beacon/endpoint/config.json", watchFull},
			guarded{filepath.Join(h, ".config", "systemd", "user"), watchBeaconEntries},
			guarded{"/etc/systemd/system", watchBeaconEntries},
		)
	case "windows":
		// For a contributor pointing the harness at a Windows VM they own. The dispatched CI
		// path does not compare snapshots at all -- see EphemeralDescription -- but a guard that
		// silently watched nothing on Windows would be indistinguishable from one that watched
		// and found nothing, which is the conflation this package exists to avoid.
		out = append(out,
			guarded{filepath.Join(programData(), "Beacon", "Endpoint", "config.json"), watchFull},
			guarded{filepath.Join(programData(), "Beacon", "Endpoint", "logs"), watchExistence},
			guarded{filepath.Join(programFiles(), "Beacon"), watchExistence},
		)
	}
	return out
}

// programData and programFiles resolve the Windows system directories, falling back to the
// conventional locations. Read from the environment because a redirected or localized install
// puts them elsewhere, and a guard watching the wrong path reports clean forever.
func programData() string {
	if v := os.Getenv("ProgramData"); v != "" {
		return v
	}
	return `C:\ProgramData`
}

func programFiles() string {
	if v := os.Getenv("ProgramFiles"); v != "" {
		return v
	}
	return `C:\Program Files`
}

// GuardedPaths returns the watched paths, for reporting and tests.
func GuardedPaths() []string {
	g := guardedPaths()
	out := make([]string, 0, len(g))
	for _, x := range g {
		out = append(out, x.path)
	}
	return out
}

// Take fingerprints the guarded state.
func Take() Snapshot {
	snap := Snapshot{Paths: map[string]string{}}
	for _, g := range guardedPaths() {
		snap.Paths[g.path] = digest(g)
	}
	snap.Services, snap.ServicesAvailable = beaconServices()
	return snap
}

func digest(g guarded) string {
	info, err := os.Lstat(g.path)
	if err != nil {
		if os.IsNotExist(err) {
			return "absent"
		}
		return "stat-error:" + err.Error()
	}

	switch g.mode {
	case watchExistence:
		// Mode but not content or size: creation and permission changes are signals, growth
		// is not.
		return fmt.Sprintf("present:mode=%s", info.Mode().String())

	case watchBeaconEntries:
		entries, err := os.ReadDir(g.path)
		if err != nil {
			return "readdir-error:" + err.Error()
		}
		var names []string
		for _, e := range entries {
			if strings.Contains(strings.ToLower(e.Name()), "beacon") {
				names = append(names, e.Name())
			}
		}
		sort.Strings(names)
		return "beacon-entries:" + strings.Join(names, ",")

	default: // watchFull
		if info.IsDir() {
			return "unexpected-dir"
		}
		b, err := os.ReadFile(g.path)
		if err != nil {
			return "read-error:" + err.Error()
		}
		h := sha256.New()
		fmt.Fprintf(h, "mode=%s;", info.Mode().String())
		h.Write(b)
		return hex.EncodeToString(h.Sum(nil))
	}
}

// beaconServices lists Beacon service labels registered for this user, and reports whether the
// probe could run at all.
//
// The unavailable case used to be encoded as a fake service name ("launchctl-unavailable"), which
// Compare then treated as a real service appearing or disappearing. A flaky probe on a machine that
// already runs Beacon services therefore produced a safety.host_untouched failure -- a host-escape
// finding manufactured by the guard rather than caused by the sandbox, and on the most serious
// check this tool has. Reported by Cursor Bugbot.
func beaconServices() (services []string, available bool) {
	var out []string
	switch runtime.GOOS {
	case "darwin":
		b, err := exec.Command("launchctl", "list").Output()
		if err != nil {
			return nil, false
		}
		for _, line := range strings.Split(string(b), "\n") {
			if !strings.Contains(strings.ToLower(line), "beacon") {
				continue
			}
			// Label is the last field; the pid column changes on restart and must not be
			// treated as a state change.
			if fields := strings.Fields(line); len(fields) > 0 {
				out = append(out, fields[len(fields)-1])
			}
		}
	case "linux":
		b, err := exec.Command("systemctl", "list-units", "--all", "--no-legend", "--plain").Output()
		if err != nil {
			return nil, false
		}
		for _, line := range strings.Split(string(b), "\n") {
			if !strings.Contains(strings.ToLower(line), "beacon") {
				continue
			}
			if fields := strings.Fields(line); len(fields) > 0 {
				out = append(out, fields[0])
			}
		}
	case "windows":
		// `sc.exe query type= service state= all` lists every service; the display name lines are
		// what carry "beacon". Parsed rather than piped through PowerShell so the probe does not
		// depend on which shell is installed.
		b, err := exec.Command("sc.exe", "query", "type=", "service", "state=", "all").Output()
		if err != nil {
			return nil, false
		}
		for _, line := range strings.Split(string(b), "\n") {
			name, ok := strings.CutPrefix(strings.TrimSpace(line), "SERVICE_NAME:")
			if !ok {
				continue
			}
			if name = strings.TrimSpace(name); strings.Contains(strings.ToLower(name), "beacon") {
				out = append(out, name)
			}
		}
	}
	sort.Strings(out)
	return out, true
}

// Diff describes what changed between two snapshots.
type Diff struct {
	Changed      []string
	NewServices  []string
	GoneServices []string
	// ServicesUnverified is true when the service probe could not run on one or both sides, so
	// no claim is made about services either way.
	ServicesUnverified bool
}

// Clean reports whether nothing guarded changed.
func (d Diff) Clean() bool {
	return len(d.Changed) == 0 && len(d.NewServices) == 0 && len(d.GoneServices) == 0
}

// Describe renders the diff for a report.
// CleanDescription is what Describe returns when nothing changed. Exported because the check
// layer must distinguish "compared, and clean" from "never compared" -- comparing against a
// duplicated string literal is how those two silently became the same thing.
const CleanDescription = "host state unchanged"

// PartialDescription is returned when nothing was seen to change but the service probe could not
// run, so the service half of the guard is unverified. Distinct from CleanDescription because
// "nothing changed" and "nothing changed that I could see" are different claims, and the check
// layer must be able to tell them apart without string-sniffing.
const PartialDescription = "files unchanged; service probe unavailable, so services are unverified"

// EphemeralDescription is recorded when the guest *is* this machine, so comparing before and
// after cannot mean anything.
//
// A backend like that runs the scenario where the harness stands, which makes the install the
// scenario is testing indistinguishable from the escape this guard looks for -- a correct run
// would report the most serious finding the tool has. So isolation is claimed on a different
// basis: the machine is disposable and somebody else destroys it. That is a genuinely weaker
// claim than "nothing on the developer's machine moved", and it gets its own value rather than
// borrowing CleanDescription precisely so a reader of a verdict can tell which one they got.
const EphemeralDescription = "guest is this machine; isolation rests on it being disposable rather than on a comparison"

func (d Diff) Describe() string {
	if d.Clean() {
		if d.ServicesUnverified {
			return PartialDescription
		}
		return CleanDescription
	}
	var parts []string
	for _, c := range d.Changed {
		parts = append(parts, "modified: "+c)
	}
	for _, s := range d.NewServices {
		parts = append(parts, "service appeared: "+s)
	}
	for _, s := range d.GoneServices {
		parts = append(parts, "service disappeared: "+s)
	}
	return strings.Join(parts, "; ")
}

// Compare diffs two snapshots.
func Compare(before, after Snapshot) Diff {
	var d Diff
	for path, b := range before.Paths {
		a, ok := after.Paths[path]
		if ok && a == b {
			continue
		}
		d.Changed = append(d.Changed, fmt.Sprintf("%s (%s -> %s)", path, label(b), label(a)))
	}
	sort.Strings(d.Changed)

	beforeSvc := setOf(before.Services)
	afterSvc := setOf(after.Services)
	// Only compare services when both snapshots actually probed them. Treating an unavailable
	// probe as an empty list would report every pre-existing Beacon service as having
	// disappeared, which is a host-escape finding invented by the guard.
	if before.ServicesAvailable && after.ServicesAvailable {
		for s := range afterSvc {
			if !beforeSvc[s] {
				d.NewServices = append(d.NewServices, s)
			}
		}
		for s := range beforeSvc {
			if !afterSvc[s] {
				d.GoneServices = append(d.GoneServices, s)
			}
		}
	} else {
		d.ServicesUnverified = true
	}

	sort.Strings(d.NewServices)
	sort.Strings(d.GoneServices)
	return d
}

func setOf(xs []string) map[string]bool {
	m := map[string]bool{}
	for _, x := range xs {
		m[x] = true
	}
	return m
}

// label makes a digest readable, so "absent -> present" is obvious in a report.
func label(digest string) string {
	switch {
	case digest == "":
		return "not-snapshotted"
	case digest == "absent":
		return "absent"
	case strings.HasPrefix(digest, "present:"),
		strings.HasPrefix(digest, "beacon-entries:"),
		strings.HasPrefix(digest, "stat-error"),
		strings.HasPrefix(digest, "read-error"),
		strings.HasPrefix(digest, "readdir-error"):
		return digest
	default:
		if len(digest) > 8 {
			return "present:" + digest[:8]
		}
		return digest
	}
}
