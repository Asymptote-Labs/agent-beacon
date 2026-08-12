// Package image defines the environments a scenario runs in.
//
// Two layers, so re-running is cheap: a base that changes only when the distro or the
// pinned Claude Code version changes, and a golden layer carrying the Beacon artifacts
// under test. The provider caches both by content, so editing Beacon rebuilds only the
// second.
package image

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/asymptote-labs/agent-beacon/beacon-sandbox/sandbox"
)

const (
	// AgentUser is unprivileged on purpose: Claude Code refuses
	// --dangerously-skip-permissions when running as root.
	AgentUser = "agent"
	// AgentUID is pinned so the local and NSS-only lanes produce the same numeric identity, and so
	// ownership assertions mean the same thing in both. 1001 because ubuntu:24.04 already ships an
	// `ubuntu` account at 1000.
	AgentUID  = 1001
	AgentHome = "/home/agent"
	WorkDir   = "/home/agent/work"
	BeaconDir = "/opt/beacon/bin"

	// DefaultClaudeVersion pins Claude Code so a verdict is attributable to a specific
	// build. Empty string means "latest", which makes runs non-reproducible.
	DefaultClaudeVersion = "2.1.220"

	// DefaultBase is the only distro in the matrix today. M0 established that Modal
	// offers no arch selection, so this is amd64.
	DefaultBase = "ubuntu:24.04"
)

// Layout is where things live inside the guest.
//
// Split out from the package constants because those describe one environment -- the Linux
// image this package builds -- and Windows runs somewhere this package does not provision at
// all. The CI job places the binaries and creates the work directory there, so the harness
// needs to *describe* the layout without owning it.
type Layout struct {
	Platform sandbox.Platform
	// AgentUser is the account the session runs as. Empty means "whoever is already running",
	// which is the Windows case: there is no way to become another user without a password.
	AgentUser string
	AgentHome string
	WorkDir   string
	BeaconDir string
	// PathPrepend is prepended to PATH so both the agent and Beacon are reachable.
	PathPrepend []string
}

// LinuxLayout is the layout of the image this package builds.
func LinuxLayout() Layout {
	return Layout{
		Platform:    sandbox.PlatformPosix,
		AgentUser:   AgentUser,
		AgentHome:   AgentHome,
		WorkDir:     WorkDir,
		BeaconDir:   BeaconDir,
		PathPrepend: PathPrepend(),
	}
}

// WindowsLayout is the layout the windows-sandbox CI job provisions.
//
// No AgentUser: the runner is already an administrator with UAC disabled, and this backend
// cannot switch users anyway. Whether that is acceptable to the agent is an open question the
// w00-probe scenario answers -- Claude Code refuses --dangerously-skip-permissions as root on
// POSIX, and if it refuses for an elevated Windows user too, the CI job will have to create a
// standard account and run the whole harness as them.
func WindowsLayout() Layout {
	home := windowsHome()
	beaconDir := `C:\beacon-sandbox\bin`
	return Layout{
		Platform:  sandbox.PlatformWindows,
		AgentHome: home,
		WorkDir:   `C:\beacon-sandbox\work`,
		BeaconDir: beaconDir,
		// install.ps1 puts the agent in %USERPROFILE%\.local\bin -- the same ~/.local/bin
		// convention the Linux image uses, not a Windows-specific Programs directory. Assuming
		// otherwise cost a probe run: the install succeeded, `claude` was not on PATH, and the
		// failure read as the agent being missing rather than as a wrong PATH entry. The
		// installer prints its location, and this matches what it printed.
		PathPrepend: []string{beaconDir, filepath.Join(home, ".local", "bin")},
	}
}

// LayoutFor selects the layout for a platform.
func LayoutFor(p sandbox.Platform) Layout {
	if p.IsWindows() {
		return WindowsLayout()
	}
	return LinuxLayout()
}

// PrivilegedUser names the account a privileged step runs as, or empty when this platform has
// no account to switch to.
//
// Windows is the empty case, and that is not a gap: the runner is already an administrator with
// UAC disabled, so a system-mode install has the rights it needs without becoming anyone. There
// is also no way to become another user without a password, so asking would fail rather than
// escalate.
func (l Layout) PrivilegedUser() string {
	if l.Platform.IsWindows() {
		return ""
	}
	return "root"
}

// DefaultLogPath is where Beacon writes the runtime log when `endpoint status --json` could not
// be parsed.
//
// A fallback only. The install scenarios read the real path out of status, because a scenario
// that hardcodes the expected path agrees with itself rather than with Beacon -- which is the
// one thing an install scenario exists to check.
func (l Layout) DefaultLogPath(system bool) string {
	if l.Platform.IsWindows() {
		if system {
			programData := os.Getenv("ProgramData")
			if programData == "" {
				programData = `C:\ProgramData`
			}
			return filepath.Join(programData, "Beacon", "Endpoint", "logs", "runtime.jsonl")
		}
		return filepath.Join(l.AgentHome, ".beacon", "endpoint", "logs", "runtime.jsonl")
	}
	if system {
		return "/var/log/beacon-agent/runtime.jsonl"
	}
	return l.AgentHome + "/.beacon/endpoint/logs/runtime.jsonl"
}

// ConsoleUser names the human whose agent runtime a system-mode install must configure.
//
// A system endpoint runs elevated, and `endpoint install` configures the *installing* user's
// settings -- which for a package install is not the person at the keyboard. Beacon resolves
// that person from SUDO_USER and then logind; a system-mode scenario has to name them or its
// capture assertions would be testing an unused settings file. On Windows there is one account,
// so it is that one.
func (l Layout) ConsoleUser() string {
	if l.AgentUser != "" {
		return l.AgentUser
	}
	if v := os.Getenv("USERNAME"); v != "" {
		return v
	}
	if v := os.Getenv("USER"); v != "" {
		return v
	}
	return filepath.Base(l.AgentHome)
}

// windowsHome resolves the guest home directory.
//
// USERPROFILE is the value Windows programs actually read. Falling back to a literal rather
// than to an error keeps a dispatched run from failing on a runner that has not exported it;
// the layout is only ever used to build guest paths, so a wrong guess surfaces immediately as
// a missing file rather than silently.
func windowsHome() string {
	if v := os.Getenv("USERPROFILE"); v != "" {
		return v
	}
	if v := os.Getenv("HOME"); v != "" {
		return v
	}
	return `C:\Users\runneradmin`
}

// Spec parameterizes the environment.
type Spec struct {
	Base          string
	ClaudeVersion string
	// RepoRoot locates the Beacon build artifacts.
	RepoRoot string
	// WithDocker adds a Docker daemon, for scenarios that need a real systemd as PID 1.
	//
	// Modal cannot provide that directly -- its own init holds PID 1 in both lanes -- but systemd
	// can be PID 1 inside a privileged container, so systemd scenarios run nested. This layer is
	// opt-in because it is a large install that every other scenario would pay for and none of
	// them need.
	WithDocker bool

	// NSSOnlyUser creates the agent account in an NSS source instead of /etc/passwd, reproducing
	// a directory-backed fleet. See scenario.Install.NSSOnlyUser for why that distinction decides
	// whether a system-mode install can configure anyone at all.
	NSSOnlyUser bool
}

// LinuxArtifacts are the binaries the sandbox needs, as host paths.
type LinuxArtifacts struct {
	Beacon    string
	Collector string
}

// Artifacts resolves the Linux build outputs, with actionable errors since these are produced
// by separate build steps rather than by the sandbox itself.
//
// The Beacon binary must come from the working tree -- that is the thing under test. The
// collector may be auto-resolved from a release, because it only needs rebuilding when the
// exporter itself changed.
func Artifacts(repoRoot string, log func(string, ...any)) (LinuxArtifacts, error) {
	a := LinuxArtifacts{Beacon: BeaconPath(repoRoot)}
	fi, err := os.Stat(a.Beacon)
	if err != nil {
		return a, fmt.Errorf("missing %s -- build it with: %s", a.Beacon, BuildBeaconHint)
	}
	// A zero-byte file is what an interrupted or failed build leaves behind. Rejecting it here
	// beats a confusing exec failure inside the sandbox.
	if fi.Size() == 0 {
		return a, fmt.Errorf("%s is empty, so the build did not complete -- rebuild with: %s",
			a.Beacon, BuildBeaconHint)
	}
	collector, err := EnsureCollector(repoRoot, log)
	if err != nil {
		return a, err
	}
	a.Collector = collector
	return a, nil
}

// Build returns the provider-neutral image spec.
func Build(spec Spec, log func(string, ...any)) (sandbox.ImageSpec, error) {
	if spec.Base == "" {
		spec.Base = DefaultBase
	}
	if spec.ClaudeVersion == "" {
		spec.ClaudeVersion = DefaultClaudeVersion
	}
	// The version is interpolated into a single-quoted shell command in the image layer, so a
	// value containing a quote would break the quoting and change what gets executed. Claude Code
	// versions are dotted numerals, so anything else is a mistake worth rejecting outright rather
	// than passing to a shell. Reported by the Copilot reviewer.
	if !validClaudeVersion(spec.ClaudeVersion) {
		return sandbox.ImageSpec{}, fmt.Errorf("invalid --claude-version %q: expected a dotted numeric version "+
			"such as %s", spec.ClaudeVersion, DefaultClaudeVersion)
	}
	art, err := Artifacts(spec.RepoRoot, log)
	if err != nil {
		return sandbox.ImageSpec{}, err
	}

	pkgs := "curl ca-certificates git tar ripgrep procps sudo python3 jq"
	if spec.NSSOnlyUser {
		// libnss-extrausers is a real NSS module that reads its own database, which is exactly the
		// property a directory service has and /etc/passwd does not. It reproduces OpenLDAP/SSSD
		// account visibility without running a directory server, so the lane stays cheap enough to
		// be a regression test rather than an occasional manual exercise.
		pkgs += " libnss-extrausers"
	}
	if spec.WithDocker {
		// docker.io rather than the upstream repo: it is one apt line, and the daemon only has
		// to be new enough to run a privileged container with a host cgroup namespace.
		//
		// systemd is installed here, in the image, even though nothing in the sandbox itself can
		// use it -- the provider's init owns PID 1. It is here because the nested container is
		// built by importing this very filesystem, and an image build is the only moment with
		// unrestricted network. Installing it later, inside the nested build, is what the first
		// attempt did: the pull succeeded because the daemon shares the sandbox's network, then
		// apt failed because a build container's traffic is NATed through docker0 and the
		// sandbox's egress allowlist does not cover it.
		pkgs += " docker.io iproute2 systemd systemd-sysv dbus"
	}

	layers := []string{
		"ENV DEBIAN_FRONTEND=noninteractive",
		"RUN apt-get update -qq && apt-get install -y -qq " + pkgs + " >/dev/null",
	}
	layers = append(layers, accountLayers(spec.NSSOnlyUser)...)
	layers = append(layers,
		"RUN mkdir -p "+BeaconDir,
		// Claude Code's native installer needs no Node. Pinned for reproducibility, and
		// installed as the agent user so it lands in that user's ~/.local/bin.
		fmt.Sprintf("RUN su - %s -c 'curl -fsSL https://claude.ai/install.sh | bash -s %s'",
			AgentUser, spec.ClaudeVersion),
		// A seeded git repo gives scenarios somewhere realistic to work, and exercises
		// Beacon's repository/branch enrichment.
		fmt.Sprintf("RUN su - %s -c 'mkdir -p %s/repo && cd %s/repo && git init -q && "+
			"git config user.email a@b.c && git config user.name beacon-sandbox && "+
			"printf \"# fixture\\n\" > README.md && git add -A && git commit -qm initial'",
			AgentUser, WorkDir, WorkDir),
	)

	if spec.WithDocker {
		// Group membership is set at image build time; adding it later would need a re-login to
		// take effect, and the session runs as this user.
		layers = append(layers,
			fmt.Sprintf("RUN usermod -aG docker %s", AgentUser))
	}

	return sandbox.ImageSpec{
		Base:   spec.Base,
		Layers: layers,
		Files: map[string]string{
			BeaconDir + "/beacon":         art.Beacon,
			BeaconDir + "/beacon-otelcol": art.Collector,
		},
	}, nil
}

// accountLayers create the session account, either locally or visible only through NSS.
//
// The two forms differ in exactly one respect -- whether the account appears in /etc/passwd -- and
// are otherwise identical in username, uid, home directory and shell. That is deliberate: a
// scenario comparing an NSS-only lane against a local one is then comparing account *visibility*
// and nothing else, so a difference in outcome has one available explanation.
func accountLayers(nssOnly bool) []string {
	if !nssOnly {
		return []string{
			fmt.Sprintf("RUN useradd -m -u %d -s /bin/bash %s && mkdir -p %s && chown -R %s:%s %s",
				AgentUID, AgentUser, WorkDir, AgentUser, AgentUser, AgentHome),
		}
	}
	return []string{
		// files first, so root and the system accounts still resolve normally; extrausers supplies
		// only what files does not have. This is the same ordering a real SSSD host uses.
		"RUN sed -i -e 's/^passwd:.*/passwd:         files extrausers/' " +
			"-e 's/^group:.*/group:          files extrausers/' " +
			"-e 's/^shadow:.*/shadow:         files extrausers/' /etc/nsswitch.conf",
		fmt.Sprintf("RUN mkdir -p /var/lib/extrausers && "+
			"printf '%s:x:%d:%d::%s:/bin/bash\\n' > /var/lib/extrausers/passwd && "+
			"printf '%s:x:%d:\\n' > /var/lib/extrausers/group && "+
			"printf '%s:!:20000:0:99999:7:::\\n' > /var/lib/extrausers/shadow && "+
			"chmod 0644 /var/lib/extrausers/passwd /var/lib/extrausers/group && "+
			"chmod 0640 /var/lib/extrausers/shadow && "+
			"mkdir -p %s %s && chown -R %d:%d %s",
			AgentUser, AgentUID, AgentUID, AgentHome,
			AgentUser, AgentUID,
			AgentUser,
			AgentHome, WorkDir, AgentUID, AgentUID, AgentHome),
		// Assert the lane actually has the property it exists to provide. Without this, a broken
		// NSS module or a changed nsswitch format would leave the account resolvable the ordinary
		// way, and the scenario would pass for the wrong reason -- reporting that a bug is fixed
		// when it was merely never exercised. Failing at image build is far cheaper than failing
		// that conclusion.
		fmt.Sprintf("RUN getent passwd %s >/dev/null || "+
			"{ echo 'nss lane broken: getent cannot resolve %s' >&2; exit 1; }",
			AgentUser, AgentUser),
		fmt.Sprintf("RUN if grep -q '^%s:' /etc/passwd; then "+
			"echo 'nss lane broken: %s leaked into /etc/passwd' >&2; exit 1; fi",
			AgentUser, AgentUser),
	}
}

// PostPushLayers are the commands that must run after the artifact files land. The
// provider snapshots after Put, so these run in the first launched instance instead.
func PostPushLayers() string {
	return fmt.Sprintf("chmod 0755 %s/beacon %s/beacon-otelcol && "+
		"ln -sf %s/beacon /usr/local/bin/beacon && "+
		"ln -sf %s/beacon-otelcol /usr/local/bin/beacon-otelcol",
		BeaconDir, BeaconDir, BeaconDir, BeaconDir)
}

// PathPrepend is the PATH the agent user needs to reach both claude and beacon.
func PathPrepend() []string {
	return []string{AgentHome + "/.local/bin", BeaconDir, "/usr/local/bin"}
}

// validClaudeVersion accepts only dotted numerals, the shape every Claude Code release uses.
//
// Deliberately a whitelist rather than an escape: the value reaches a shell inside the image
// layer, and rejecting an unexpected shape is easier to reason about than proving an escaping
// scheme is airtight.
func validClaudeVersion(v string) bool {
	if v == "" || strings.HasPrefix(v, ".") || strings.HasSuffix(v, ".") || strings.Contains(v, "..") {
		return false
	}
	for _, r := range v {
		if (r < '0' || r > '9') && r != '.' {
			return false
		}
	}
	return true
}
