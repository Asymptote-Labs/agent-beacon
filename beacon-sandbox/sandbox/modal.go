package sandbox

import (
	"context"
	"fmt"
	"io"
	"sort"
	"strings"
	"sync"
	"time"

	modal "github.com/modal-labs/modal-client/go"
)

// Modal is the Provider backed by Modal sandboxes.
//
// Facts established empirically in m0 that this implementation depends on:
//   - Images build from raw Dockerfile lines; there is no build-context COPY, so
//     artifact files are pushed into a live instance and snapshotted instead.
//   - Exec returns truthful exit codes with separate stdout/stderr.
//   - Timeout defaults to 5 minutes; exceeding it kills the instance and subsequent
//     Exec calls fail with "The Sandbox is unavailable".
//   - Neither lane gives the entrypoint PID 1.
type Modal struct {
	client *modal.Client
	app    *modal.App
}

type modalImage struct{ img *modal.Image }

func (m modalImage) Ref() string { return m.img.ImageID }

type modalInstance struct{ sb *modal.Sandbox }

func (m modalInstance) ID() string { return m.sb.SandboxID }

// NewModal connects using the ambient Modal credentials (MODAL_TOKEN_ID/SECRET, or the
// active profile in ~/.modal.toml) and resolves the app, creating it if absent.
func NewModal(ctx context.Context, appName string) (*Modal, error) {
	client, err := modal.NewClient()
	if err != nil {
		return nil, fmt.Errorf("modal client: %w", err)
	}
	app, err := client.Apps.FromName(ctx, appName, &modal.AppFromNameParams{CreateIfMissing: true})
	if err != nil {
		client.Close()
		return nil, fmt.Errorf("modal app %q: %w", appName, err)
	}
	return &Modal{client: client, app: app}, nil
}

func (m *Modal) Close() {
	if m.client != nil {
		m.client.Close()
	}
}

func (m *Modal) Name() string { return "modal" }

func (m *Modal) EnsureImage(ctx context.Context, spec ImageSpec) (Image, error) {
	img := m.client.Images.FromRegistry(spec.Base, nil)
	if len(spec.Layers) > 0 {
		img = img.DockerfileCommands(spec.Layers, nil)
	}
	built, err := img.Build(ctx, m.app, nil)
	if err != nil {
		return nil, fmt.Errorf("build image from %s: %w", spec.Base, err)
	}
	if len(spec.Files) == 0 {
		return modalImage{built}, nil
	}

	// No build-context COPY in the SDK: stage the files into a live instance and
	// snapshot. Modal content-addresses the result, so this is cached like any layer.
	//
	// The timeout has to cover pushing every artifact plus the snapshot, not just the push.
	// At 8 minutes this intermittently expired mid-stage on ~67MB of binaries, and the
	// failure surfaces as a confusing "Sandbox has already shut down" from the *next* call
	// rather than as a timeout, so it is worth being generous here.
	inst, err := m.Launch(ctx, modalImage{built}, LaunchSpec{
		CPU: 2, MemLimitMiB: 2048, Timeout: 25 * time.Minute,
	})
	if err != nil {
		return nil, fmt.Errorf("stage instance for file layer: %w", err)
	}
	defer m.Terminate(ctx, inst)

	// Deterministic order so the resulting snapshot is content-addressed consistently
	// across runs; map iteration order would otherwise defeat Modal's image cache.
	remotes := make([]string, 0, len(spec.Files))
	for remote := range spec.Files {
		remotes = append(remotes, remote)
	}
	sort.Strings(remotes)
	for _, remote := range remotes {
		if err := m.Put(ctx, inst, spec.Files[remote], remote); err != nil {
			return nil, fmt.Errorf("stage %s -> %s: %w", spec.Files[remote], remote, err)
		}
	}
	return m.Snapshot(ctx, inst)
}

func (m *Modal) Snapshot(ctx context.Context, inst Instance) (Image, error) {
	mi, ok := inst.(modalInstance)
	if !ok {
		return nil, fmt.Errorf("snapshot: unexpected instance type %T", inst)
	}
	snap, err := mi.sb.SnapshotFilesystem(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("snapshot filesystem: %w", err)
	}
	return modalImage{snap}, nil
}

func (m *Modal) Launch(ctx context.Context, img Image, spec LaunchSpec) (Instance, error) {
	mi, ok := img.(modalImage)
	if !ok {
		return nil, fmt.Errorf("launch: unexpected image type %T", img)
	}

	params := &modal.SandboxCreateParams{
		CPU:       spec.CPU,
		MemoryMiB: spec.MemLimitMiB,
		Timeout:   spec.Timeout,
		Workdir:   spec.Workdir,
		Env:       spec.Env,
	}
	params.Command = spec.Command
	if len(params.Command) == 0 {
		params.Command = []string{"sleep", "infinity"}
	}
	if spec.Lane == LaneVM {
		params.ExperimentalOptions = map[string]any{"vm_runtime": true}
	}
	// Named secrets first, then inline ones. Modal applies later secrets over earlier, so an
	// explicitly supplied value wins over a stored one of the same key.
	for _, name := range spec.SecretNames {
		sec, err := m.client.Secrets.FromName(ctx, name, nil)
		if err != nil {
			return nil, fmt.Errorf("resolve secret %q: %w (create it with `modal secret create %s ANTHROPIC_API_KEY=…`)", name, err, name)
		}
		params.Secrets = append(params.Secrets, sec)
	}
	if len(spec.Secrets) > 0 {
		sec, err := m.client.Secrets.FromMap(ctx, spec.Secrets, nil)
		if err != nil {
			return nil, fmt.Errorf("create secret: %w", err)
		}
		params.Secrets = append(params.Secrets, sec)
	}
	switch {
	case spec.BlockEgress:
		params.BlockNetwork = true
	case len(spec.EgressAllowDomains) > 0:
		params.OutboundDomainAllowlist = &modal.Allowlist{Entries: spec.EgressAllowDomains}
	}

	sb, err := m.client.Sandboxes.Create(ctx, m.app, mi.img, params)
	if err != nil {
		return nil, fmt.Errorf("create sandbox: %w", err)
	}
	return modalInstance{sb}, nil
}

func (m *Modal) Exec(ctx context.Context, inst Instance, script string, opts ExecOpts) (Result, error) {
	mi, ok := inst.(modalInstance)
	if !ok {
		return Result{}, fmt.Errorf("exec: unexpected instance type %T", inst)
	}
	if opts.Timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, opts.Timeout)
		defer cancel()
	}

	argv := buildArgv(script, opts)
	p, err := mi.sb.Exec(ctx, argv, nil)
	if err != nil {
		return Result{}, fmt.Errorf("exec: %w", err)
	}
	out, errb, outErr, errErr := drainStreams(p.Stdout, p.Stderr)

	code, werr := p.Wait(ctx, nil)
	res := Result{ExitCode: code, Stdout: string(out), Stderr: string(errb)}
	if werr != nil {
		return res, fmt.Errorf("wait: %w", werr)
	}
	// Surfaced rather than ignored: a truncated stream means the captured output is incomplete,
	// and this output is what every verdict is built from.
	if outErr != nil {
		return res, fmt.Errorf("read stdout: %w", outErr)
	}
	if errErr != nil {
		return res, fmt.Errorf("read stderr: %w", errErr)
	}
	return res, nil
}

// drainStreams reads stdout and stderr concurrently.
//
// Concurrency is the whole point, not a nicety. Reading stdout to completion before touching
// stderr deadlocks whenever the guest writes enough to stderr to fill its pipe buffer: the process
// blocks on that write, so it never exits and never closes stdout, so the read never returns.
// Verbose failures are exactly when that happens, which is exactly when this tool is being used to
// find out what went wrong. Reported by the Copilot reviewer.
//
// Read errors are returned rather than discarded: a truncated stream means the captured output is
// incomplete, and every verdict is built from that output.
func drainStreams(stdout, stderr io.Reader) (out, errb []byte, outErr, errErr error) {
	var wg sync.WaitGroup
	wg.Add(2)
	go func() { defer wg.Done(); out, outErr = io.ReadAll(stdout) }()
	go func() { defer wg.Done(); errb, errErr = io.ReadAll(stderr) }()
	wg.Wait()
	return out, errb, outErr, errErr
}

// buildArgv wraps a shell script for the requested user and environment.
//
// `su -` (login shell) resets the environment and strips injected secrets, which silently
// broke an early run: Claude Code could not authenticate, so it never ran the tool and the
// telemetry gap looked like a Beacon bug. `su -p` preserves the environment, but then HOME
// and PATH must be set explicitly. The secret value itself is never placed in argv.
func buildArgv(script string, opts ExecOpts) []string {
	var pre []string
	if opts.HomeDir != "" {
		pre = append(pre, "export HOME="+shellQuote(opts.HomeDir))
	}
	if len(opts.PathPrepend) > 0 {
		pre = append(pre, "export PATH="+shellQuote(strings.Join(opts.PathPrepend, ":"))+":$PATH")
	}
	if opts.Dir != "" {
		pre = append(pre, "cd "+shellQuote(opts.Dir))
	}
	full := script
	if len(pre) > 0 {
		full = strings.Join(pre, "; ") + "; " + script
	}

	if opts.User == "" || opts.User == "root" {
		return []string{"bash", "-c", full}
	}
	flag := "-"
	if opts.PreserveEnv {
		flag = "-p"
	}
	return []string{"su", flag, opts.User, "-c", full}
}

func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

func (m *Modal) Put(ctx context.Context, inst Instance, localPath, remotePath string) error {
	mi, ok := inst.(modalInstance)
	if !ok {
		return fmt.Errorf("put: unexpected instance type %T", inst)
	}
	return mi.sb.Filesystem.CopyFromLocal(ctx, localPath, remotePath, nil)
}

func (m *Modal) Get(ctx context.Context, inst Instance, remotePath, localPath string) error {
	mi, ok := inst.(modalInstance)
	if !ok {
		return fmt.Errorf("get: unexpected instance type %T", inst)
	}
	return mi.sb.Filesystem.CopyToLocal(ctx, remotePath, localPath, nil)
}

func (m *Modal) Terminate(ctx context.Context, inst Instance) error {
	mi, ok := inst.(modalInstance)
	if !ok {
		return fmt.Errorf("terminate: unexpected instance type %T", inst)
	}
	_, err := mi.sb.Terminate(ctx, nil)
	return err
}
