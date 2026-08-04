// Package sandbox abstracts "somewhere disposable to run a Beacon + agent session".
//
// Only this package knows about Modal. Everything above it works against Provider, so
// adding a backend later (the deferred Lume/macOS substrate, a plain SSH host, another
// cloud) is one file and no redesign.
package sandbox

import (
	"context"
	"time"
)

// Lane selects the isolation technology. Modal's default gVisor lane is fast and cheap
// but cannot host an init system; the VM lane has a real kernel. M0 established that
// neither makes your entrypoint PID 1, so systemd work needs the nested recipe.
type Lane string

const (
	LaneDefault Lane = "gvisor"
	LaneVM      Lane = "vm"
)

// ImageSpec describes a reproducible environment. Layers are ordered and cached by the
// provider, so an unchanged prefix is free on re-run.
type ImageSpec struct {
	// Base registry reference, e.g. "ubuntu:24.04".
	Base string
	// Layers are provider-neutral build steps. The Modal provider renders these as
	// Dockerfile lines; another provider might script them over SSH.
	Layers []string
	// Files pushed in after the layers, keyed by remote path. Used for build artifacts
	// that have no stable registry location -- the Beacon binaries under test.
	Files map[string]string
}

// LaunchSpec describes one disposable instance.
type LaunchSpec struct {
	Lane        Lane
	CPU         float64
	MemLimitMiB int
	Timeout     time.Duration
	Workdir     string
	// Command is the entrypoint. Empty means "stay alive and wait for Exec".
	Command []string
	// Env is non-secret environment.
	Env map[string]string
	// Secrets are injected as environment but must never reach argv, an image layer,
	// or a collected artifact.
	Secrets map[string]string
	// SecretNames reference secrets already stored with the provider, by name. The value
	// never passes through this process, which is the safest option -- at the cost of the
	// leak check having no value to search collected artifacts for, so it degrades to
	// "unverified" rather than falsely reporting clean.
	SecretNames []string
	// EgressAllowDomains restricts outbound network to these domains. Empty means
	// unrestricted; BlockEgress overrides it entirely. Loopback always works, which is
	// what the local collector needs.
	EgressAllowDomains []string
	BlockEgress        bool
}

// ExecOpts tunes a single command.
type ExecOpts struct {
	// User runs the command as an unprivileged account. Claude Code refuses
	// --dangerously-skip-permissions as root, so sessions must not run as root.
	User string
	// Dir is the working directory.
	Dir string
	// Timeout bounds this command, independent of the instance lifetime.
	Timeout time.Duration
	// PreserveEnv keeps the caller's environment when switching user. Without it a
	// login shell resets the environment and strips injected secrets -- this silently
	// broke the first M0 run.
	PreserveEnv bool
	// HomeDir is exported as HOME, needed when PreserveEnv keeps the caller's HOME.
	HomeDir string
	// PathPrepend is prepended to PATH.
	PathPrepend []string
}

// Result is the outcome of one command.
type Result struct {
	ExitCode int
	Stdout   string
	Stderr   string
}

// Image is an opaque provider handle to a built environment.
type Image interface {
	// Ref is a stable identifier, recorded in run metadata for reproducibility.
	Ref() string
}

// Instance is a live disposable environment.
type Instance interface {
	// ID is a stable identifier for logs and debugging.
	ID() string
}

// Provider is the whole backend contract.
type Provider interface {
	// Name identifies the backend in run metadata.
	Name() string

	// EnsureImage builds or resolves an environment. Implementations should be
	// idempotent and lean on content-addressed caching.
	EnsureImage(ctx context.Context, spec ImageSpec) (Image, error)

	// Snapshot captures an instance's filesystem as a reusable image. This is how a
	// "golden" layer is produced when artifacts cannot be baked in at build time.
	Snapshot(ctx context.Context, inst Instance) (Image, error)

	Launch(ctx context.Context, img Image, spec LaunchSpec) (Instance, error)
	Exec(ctx context.Context, inst Instance, script string, opts ExecOpts) (Result, error)
	Put(ctx context.Context, inst Instance, localPath, remotePath string) error
	Get(ctx context.Context, inst Instance, remotePath, localPath string) error
	Terminate(ctx context.Context, inst Instance) error
}
