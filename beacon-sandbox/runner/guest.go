package runner

import (
	"context"
	"encoding/base64"
	"fmt"
	"path"
	"strings"
	"sync/atomic"
	"time"

	"github.com/asymptote-labs/agent-beacon/beacon-sandbox/sandbox"
)

// guest is "the environment under test" -- where Beacon gets installed, where the agent session
// runs, and where the runtime log lands.
//
// For almost every scenario that is the sandbox instance itself. Scenarios that need a real
// systemd get a nested privileged container instead, because the sandbox provider's own init
// holds PID 1 and systemd refuses to start as anything else.
//
// Both sit behind one interface so everything above it -- canary, sentinel, quiescence,
// collection, verdict -- is byte-for-byte the same code. A systemd run is therefore judged by
// exactly the same rules as every other run, rather than by a parallel path that could drift into
// being more forgiving.
type guest interface {
	Exec(ctx context.Context, script string, opts sandbox.ExecOpts) (sandbox.Result, error)
	// Get copies a file out of the guest to a local path.
	Get(ctx context.Context, remotePath, localPath string) error
	// Describe names the environment for run metadata, so a verdict says where it ran.
	Describe() string
}

// directGuest runs commands in the sandbox instance itself.
type directGuest struct {
	p    sandbox.Provider
	inst sandbox.Instance
}

func (g directGuest) Exec(ctx context.Context, script string, opts sandbox.ExecOpts) (sandbox.Result, error) {
	return g.p.Exec(ctx, g.inst, script, opts)
}

func (g directGuest) Get(ctx context.Context, remotePath, localPath string) error {
	return g.p.Get(ctx, g.inst, remotePath, localPath)
}

func (g directGuest) Describe() string { return "sandbox" }

// Names and paths for the nested lane. Fixed rather than random: a leftover container from an
// interrupted run is reclaimed by name on the next attempt instead of accumulating.
const (
	nestedContainer = "beacon-systemd"
	nestedImage     = "beacon-systemd:local"
	// /var/tmp, not /tmp. Once systemd is PID 1 it may enable tmp.mount, which mounts a tmpfs
	// over /tmp -- a file placed there earlier is then shadowed and reads as missing. That
	// wasted real time in the packaging smoke test before it was understood.
	nestedStageDir = "/var/tmp/beacon-nested"
	// nestedStartMarker is how a script that ran is told apart from one that never did.
	nestedStartMarker = "__BEACON_NESTED_STARTED__"
)

// nestedGuest runs commands inside a privileged container where systemd is genuinely PID 1.
//
// Why nested at all: the sandbox provider's init occupies PID 1 in both of its lanes, and systemd
// exits immediately when it is not PID 1 ("Couldn't find an alternative telinit implementation to
// spawn"). A privileged container with a host cgroup namespace can hand PID 1 to systemd, so the
// systemd surface -- unit installation, `enable --now`, `Restart=always`, `systemctl is-active` --
// is exercised for real rather than against a stub.
type nestedGuest struct {
	p    sandbox.Provider
	inst sandbox.Instance
	seq  atomic.Int64
	// dockerVersion and systemdState are recorded so a run says what it ran on.
	dockerVersion string
	systemdState  string
}

func (g *nestedGuest) Describe() string {
	return fmt.Sprintf("nested container %s (docker %s, systemd %s)",
		nestedContainer, g.dockerVersion, g.systemdState)
}

// Exec runs a script inside the container as the requested user.
//
// The script has to cross two shells: the sandbox's, then the container's. Escaping it twice is
// what broke repeatedly in the early probes -- anything holding a quote, a semicolon or a loop
// arrived mangled. So the script travels as base64, which no shell can misread, and is decoded
// into a file that the container executes directly. Nothing metacharacter-bearing ever appears on
// a command line.
func (g *nestedGuest) Exec(ctx context.Context, script string, opts sandbox.ExecOpts) (sandbox.Result, error) {
	n := g.seq.Add(1)
	stage := path.Join(nestedStageDir, fmt.Sprintf("step-%03d.sh", n))
	// The script announces itself on stderr before doing anything. Without that marker, a script
	// that never ran -- a failed copy, a stopped container -- looks exactly like a script that ran
	// and printed nothing, and the first nested run duly reported an empty `beacon version` and
	// carried on to fail somewhere less informative.
	enc := base64.StdEncoding.EncodeToString([]byte("echo " + nestedStartMarker + " >&2\n" + script))

	flags := []string{"exec"}
	if opts.User != "" {
		flags = append(flags, "-u", opts.User)
	}
	if opts.Dir != "" {
		flags = append(flags, "-w", opts.Dir)
	}
	if opts.HomeDir != "" {
		flags = append(flags, "-e", "HOME="+opts.HomeDir)
	}
	if len(opts.PathPrepend) > 0 {
		flags = append(flags, "-e", "PATH="+strings.Join(opts.PathPrepend, ":")+
			":/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin")
	}
	// Named without a value on purpose: docker then takes the value from its own environment, so
	// the credential is inherited rather than written onto a command line. Passing `-e K=v` would
	// put the key in argv, which is precisely what the argv leak check exists to catch -- the
	// harness must not be the thing that leaks it.
	for _, k := range nestedPassThroughEnv {
		flags = append(flags, "-e", k)
	}
	flags = append(flags, nestedContainer, "sh", stage)

	wrapper := fmt.Sprintf("set -e\nmkdir -p %s\nprintf %%s '%s' | base64 -d > %s\n"+
		"docker cp %s %s:%s >/dev/null\nexec docker %s\n",
		shq(nestedStageDir), enc, shq(stage),
		shq(stage), nestedContainer, shq(stage),
		strings.Join(quoteAll(flags), " "))

	// The docker CLI runs as root in the sandbox and needs its environment preserved, since that
	// is where the pass-through credentials live.
	timeout := opts.Timeout
	if timeout == 0 {
		timeout = 5 * time.Minute
	}
	r, err := g.p.Exec(ctx, g.inst, wrapper, sandbox.ExecOpts{
		User: "root", Dir: "/", Timeout: timeout, PreserveEnv: true,
	})
	if err != nil {
		return r, err
	}
	if !strings.Contains(r.Stderr, nestedStartMarker) {
		// Reported as an error rather than a non-zero result, because the two mean different
		// things to every caller: a non-zero result is a finding about Beacon, while this is the
		// harness failing to reach the container at all.
		return r, fmt.Errorf("the script never started inside %s (rc=%d): %s",
			nestedContainer, r.ExitCode, tailOf(r.Stdout+r.Stderr))
	}
	r.Stderr = strings.Replace(r.Stderr, nestedStartMarker+"\n", "", 1)
	return r, nil
}

// nestedPassThroughEnv are the variables the agent session needs inside the container. Only
// credentials Claude Code reads; nothing Beacon needs, because Beacon's configuration comes from
// the install it performs in there.
var nestedPassThroughEnv = []string{
	"ANTHROPIC_API_KEY",
	"ANTHROPIC_AUTH_TOKEN",
	"CLAUDE_CODE_OAUTH_TOKEN",
}

// Get copies a file out of the container, then out of the sandbox.
func (g *nestedGuest) Get(ctx context.Context, remotePath, localPath string) error {
	n := g.seq.Add(1)
	stage := path.Join(nestedStageDir, fmt.Sprintf("fetch-%03d", n))
	script := fmt.Sprintf("set -e\nmkdir -p %s\ndocker cp %s:%s %s\n",
		shq(nestedStageDir), nestedContainer, shq(remotePath), shq(stage))
	r, err := g.p.Exec(ctx, g.inst, script, sandbox.ExecOpts{
		User: "root", Dir: "/", Timeout: 2 * time.Minute,
	})
	if err != nil {
		return fmt.Errorf("docker cp %s out of the container: %w", remotePath, err)
	}
	if r.ExitCode != 0 {
		return fmt.Errorf("docker cp %s out of the container failed (rc=%d): %s",
			remotePath, r.ExitCode, oneLine(r.Stdout+r.Stderr))
	}
	return g.p.Get(ctx, g.inst, stage, localPath)
}

// startNested brings up dockerd and a systemd container, and refuses to return a guest that is
// not actually running systemd as PID 1.
//
// Every step reports what it saw. A half-started environment that silently degraded to "no
// service manager" would make a systemd scenario pass while testing the supervised backend, which
// is the one outcome worth going to some trouble to prevent.
func startNested(ctx context.Context, p sandbox.Provider, inst sandbox.Instance,
	logf func(string, ...any)) (*nestedGuest, error) {

	root := sandbox.ExecOpts{User: "root", Dir: "/", Timeout: 10 * time.Minute, PreserveEnv: true}
	g := &nestedGuest{p: p, inst: inst}

	logf("starting dockerd (this scenario needs systemd as PID 1, which only a privileged container can provide)")
	r, err := p.Exec(ctx, inst, dockerdScript, root)
	if err != nil || r.ExitCode != 0 {
		return nil, fmt.Errorf("dockerd did not start (rc=%d): %v %s",
			r.ExitCode, err, tailOf(r.Stdout+r.Stderr))
	}
	g.dockerVersion = oneLine(r.Stdout)
	logf("dockerd ready: %s", g.dockerVersion)

	logf("building the systemd container image")
	r, err = p.Exec(ctx, inst, nestedBuildScript(), root)
	if err != nil || r.ExitCode != 0 {
		return nil, fmt.Errorf("building the systemd image failed (rc=%d): %v %s",
			r.ExitCode, err, tailOf(r.Stdout+r.Stderr))
	}

	logf("starting the systemd container")
	r, err = p.Exec(ctx, inst, nestedRunScript(), root)
	if err != nil || r.ExitCode != 0 {
		return nil, fmt.Errorf("the systemd container did not come up (rc=%d): %v %s",
			r.ExitCode, err, tailOf(r.Stdout+r.Stderr))
	}
	// The script prints PID1=<comm> STATE=<is-system-running> and exits non-zero unless PID 1 is
	// systemd, so reaching here means the requirement is met; the values are kept for metadata.
	for _, f := range strings.Fields(oneLine(r.Stdout)) {
		if v, ok := strings.CutPrefix(f, "STATE="); ok {
			g.systemdState = v
		}
	}
	logf("systemd is PID 1 in %s (%s)", nestedContainer, oneLine(r.Stdout))

	// docker cp refuses a destination whose parent directory does not exist, and the image import
	// excludes /var/tmp, so the container starts without one. Created once here rather than per
	// exec: if it is missing, every single command fails, so this is the right place to find out.
	r, err = p.Exec(ctx, inst, fmt.Sprintf("mkdir -p %s && docker exec %s mkdir -p %s",
		shq(nestedStageDir), nestedContainer, shq(nestedStageDir)), root)
	if err != nil || r.ExitCode != 0 {
		return nil, fmt.Errorf("could not create the staging directory %s (rc=%d): %v %s",
			nestedStageDir, r.ExitCode, err, tailOf(r.Stdout+r.Stderr))
	}
	return g, nil
}

// dockerdScript starts the daemon if it is not already up, and fails loudly with its log if not.
const dockerdScript = `set -u
if docker info >/dev/null 2>&1; then
  docker version --format '{{.Server.Version}}'
  exit 0
fi
(dockerd >/var/log/dockerd.log 2>&1 &)
i=0
while [ "$i" -lt 60 ]; do
  docker info >/dev/null 2>&1 && break
  i=$((i+1)); sleep 2
done
if ! docker info >/dev/null 2>&1; then
  echo "dockerd never became ready; last 30 lines of its log:"
  tail -30 /var/log/dockerd.log 2>/dev/null || echo "(no log)"
  exit 1
fi
docker version --format '{{.Server.Version}}'
`

// nestedBuildScript turns the sandbox's own filesystem into the container image.
//
// The obvious approach -- a Dockerfile that pulls a base image and installs systemd -- does not
// work here, and the way it fails is worth recording. The pull succeeds, because the daemon shares
// the sandbox's network and the registry is reachable. Then `apt-get` fails inside the build
// container, because a build container's traffic is NATed through docker0 and the sandbox's
// outbound allowlist has no rule for it. The first attempt spent a full sandbox launch discovering
// that from a truncated apt exit code.
//
// Importing the live root filesystem instead needs no network whatsoever, and it is strictly more
// faithful: the container gets the exact Beacon binary under test, the same pinned Claude Code, the
// same seeded git fixture, and the same `agent` account with the same uid. There is no second
// environment that could drift from the one every other scenario runs in.
//
// --one-file-system is deliberately not used: it would silently drop anything the provider mounts
// separately, and a missing binary would surface much later as a confusing exec failure. The
// exclusions are named instead, so what is left out is visible.
func nestedBuildScript() string {
	return fmt.Sprintf(`set -eu
if docker image inspect %[1]s >/dev/null 2>&1; then
  echo "reusing %[1]s"
  exit 0
fi
tar -cf - --numeric-owner   --exclude=./proc --exclude=./sys --exclude=./dev --exclude=./run   --exclude=./tmp --exclude=./var/lib/docker --exclude=./var/tmp   --exclude=./mnt --exclude=./media --exclude=./lost+found   -C / . 2>/dev/null | docker import -c 'CMD ["/sbin/init"]' - %[1]s
docker image inspect %[1]s >/dev/null
`, nestedImage)
}

// nestedRunScript starts the container and proves systemd owns PID 1 before anything is installed.
//
// The flags are not incidental. --privileged plus a host cgroup namespace and a writable
// /sys/fs/cgroup are what let systemd manage units at all; the two tmpfs mounts give it the
// runtime directories it expects to own. Drop any of them and systemd starts but cannot start
// services, which presents as a mystifying unit failure much later.
//
// --stop-signal is how systemd is asked to shut down cleanly. docker import cannot carry a
// STOPSIGNAL, so it is set here rather than in the image.
func nestedRunScript() string {
	return fmt.Sprintf(`set -u
docker rm -f %[1]s >/dev/null 2>&1 || true
docker run -d --name %[1]s --privileged --cgroupns=host --stop-signal=SIGRTMIN+3   -v /sys/fs/cgroup:/sys/fs/cgroup:rw --tmpfs /run --tmpfs /run/lock   %[2]s >/dev/null || exit 1
state=""
i=0
while [ "$i" -lt 45 ]; do
  state="$(docker exec %[1]s systemctl is-system-running 2>/dev/null || true)"
  case "$state" in running|degraded) break ;; esac
  i=$((i+1)); sleep 2
done
pid1="$(docker exec %[1]s cat /proc/1/comm 2>/dev/null || true)"
echo "PID1=$pid1 STATE=$state"
if [ "$pid1" != systemd ]; then
  echo "PID 1 is not systemd, so this scenario would silently test the supervised backend instead:"
  docker logs %[1]s 2>&1 | tail -20
  exit 1
fi
`, nestedContainer, nestedImage)
}

// tailOf keeps the end of a command's output rather than its beginning.
//
// Build and daemon output is long, and the sentence that says what went wrong is the last one. The
// first version of this file reported these failures through oneLine, which truncates the head --
// so the report was pages of layer-download chatter and no error, and the actual cause took another
// full sandbox launch to find.
func tailOf(s string) string {
	lines := strings.Split(strings.TrimRight(s, "\n"), "\n")
	if len(lines) > 12 {
		lines = lines[len(lines)-12:]
	}
	return oneLine(strings.Join(lines, " | "))
}

// quoteAll shell-quotes each argument. Used for docker's own argv, which is built from values the
// harness controls but still passes through the sandbox's shell.
func quoteAll(args []string) []string {
	out := make([]string, len(args))
	for i, a := range args {
		out[i] = shq(a)
	}
	return out
}
