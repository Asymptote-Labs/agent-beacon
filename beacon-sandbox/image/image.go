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
	"strings"

	"github.com/asymptote-labs/agent-beacon/beacon-sandbox/sandbox"
)

const (
	// AgentUser is unprivileged on purpose: Claude Code refuses
	// --dangerously-skip-permissions when running as root.
	AgentUser = "agent"
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
		fmt.Sprintf("RUN useradd -m -s /bin/bash %s && mkdir -p %s && chown -R %s:%s %s",
			AgentUser, WorkDir, AgentUser, AgentUser, AgentHome),
		"RUN mkdir -p " + BeaconDir,
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
	}

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
