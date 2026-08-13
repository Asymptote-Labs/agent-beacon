// Package runner executes one scenario in a disposable environment and collects artifacts.
//
// It deliberately stops at collection: deciding the verdict is a separate, pure step over
// what landed on disk, so it can be re-run instantly and for free while iterating on what
// counts as correct.
package runner

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/asymptote-labs/agent-beacon/beacon-sandbox/credentials"
	"github.com/asymptote-labs/agent-beacon/beacon-sandbox/hostguard"
	"github.com/asymptote-labs/agent-beacon/beacon-sandbox/image"
	"github.com/asymptote-labs/agent-beacon/beacon-sandbox/sandbox"
	"github.com/asymptote-labs/agent-beacon/beacon-sandbox/scenario"
)

// Artifacts is what a run leaves on the host for the check step to read.
type Artifacts struct {
	Dir string
	// RuntimeLog is the collected Beacon JSONL.
	RuntimeLog string
	// Canary is the unique marker planted in this run's prompt.
	Canary string
	// SentinelPresent reports whether the scenario's sentinel artifact existed after.
	SentinelPresent bool
	// SentinelProbed records whether the check could run at all. False means the guest exec
	// failed, so nothing is known about whether the agent acted -- which must not be reported
	// as the agent having skipped the work.
	SentinelProbed bool
	SentinelDetail string
	// SessionOK reports whether Claude Code itself reported success.
	SessionOK bool
	// SessionKnown is false when claude-out.json could not be read or parsed, in which case
	// SessionOK carries no information. Without this, a missing result file and a genuinely
	// failed session were both SessionOK=false -- the same conflation the sentinel probe had.
	SessionKnown bool
	// HostDiff describes any change to the developer's own Beacon state. Must be empty:
	// every install and service operation is supposed to happen inside the sandbox.
	HostDiff hostguard.Diff
	// SecretInArgv is true if the injected credential was found in the guest's process
	// table. Must be false -- the README claims it never lands in argv.
	SecretInArgv bool
	// ArgvCheckRan records whether the argv scan actually executed, so a skipped check is
	// never mistaken for a passing one.
	ArgvCheckRan bool
	// Meta records what produced this run.
	Meta map[string]string
}

// Options configures a run.
type Options struct {
	RepoRoot string
	OutDir   string
	// Creds describes how Claude Code will be authenticated inside the sandbox.
	Creds credentials.Resolved
	// ClaudeVersion pins the agent build. Empty uses the image default.
	ClaudeVersion string
	// KeepInstance leaves the sandbox running for debugging.
	KeepInstance bool
	// Log receives progress lines.
	Log func(string, ...any)
}

// Run executes one scenario end to end and returns the collected artifacts.
func Run(ctx context.Context, p sandbox.Provider, sc scenario.Scenario, opts Options) (Artifacts, error) {
	logf := opts.Log
	if logf == nil {
		logf = func(string, ...any) {}
	}
	if opts.Creds.Source == credentials.SourceNone {
		return Artifacts{}, fmt.Errorf("no Anthropic credential resolved; run `beacon-sandbox doctor`")
	}

	// The dialect and the guest layout both follow from the provider's platform, never from the
	// host's runtime.GOOS -- a Mac dispatching a Windows run is the normal case, not the exception.
	sh := shellFor(p.Platform())
	lay := image.LayoutFor(p.Platform())

	canary := "BEACON_SANDBOX_" + randomToken()
	runDir := filepath.Join(opts.OutDir, fmt.Sprintf("%s-%s", sc.ID, canary[len(canary)-8:]))
	// 0700, not 0755: these artifacts retain prompt text and command output, and a
	// plant-secret self-test writes a real credential into a copy of the log. On a shared
	// machine 0755 hands all of that to every local user. Reported by the Copilot reviewer.
	if err := os.MkdirAll(runDir, 0o700); err != nil {
		return Artifacts{}, err
	}
	art := Artifacts{
		Dir:    runDir,
		Canary: canary,
		Meta: map[string]string{
			"scenario": sc.ID,
			"provider": p.Name(),
			"platform": string(p.Platform()),
		},
	}

	// Establish how isolation is proven for this run, and record it either way.
	//
	// The usual proof is a comparison: fingerprint the developer's own Beacon state before and
	// after, so a stray local install is caught rather than assumed impossible. That only means
	// something when the guest is a *different* machine. A backend that runs the scenario where
	// this process stands inverts it -- the install the scenario is testing is itself the change
	// the guard would report -- so for those the claim has to rest on the machine being
	// disposable instead, and say which of the two it relied on.
	//
	// The comparison and the meta write happen together in this defer, and deliberately so. An
	// earlier version wrote meta.json inline before returning, which meant host_state -- set
	// here, after that write -- never reached disk. The live verdict was still correct because
	// judging happens after Run returns, but an offline `verify` read the field as empty and
	// treated the host check as clean. An unrunnable check that reads as a pass is the exact
	// failure this tool exists to prevent. Writing here also means an early error return still
	// leaves the metadata behind for debugging.
	local := p.MutatesHost()
	var hostBefore hostguard.Snapshot
	if !local {
		hostBefore = hostguard.Take()
	}
	defer func() {
		if local {
			art.Meta["host_state"] = hostguard.EphemeralDescription
			art.Meta["disposability"] = sandbox.DisposabilityEvidence()
		} else {
			art.HostDiff = hostguard.Compare(hostBefore, hostguard.Take())
			art.Meta["host_state"] = art.HostDiff.Describe()
		}
		if b, err := json.MarshalIndent(art.Meta, "", "  "); err == nil {
			_ = os.WriteFile(filepath.Join(runDir, "meta.json"), append(b, '\n'), 0o600)
		}
	}()
	if local {
		logf("guest is this machine (%s); isolation rests on it being disposable: %s",
			p.Name(), sandbox.DisposabilityEvidence())
	}

	imgSpec, err := imageSpecFor(p.Platform(), sc, opts, logf)
	if err != nil {
		return art, err
	}
	img, err := p.EnsureImage(ctx, imgSpec)
	if err != nil {
		return art, fmt.Errorf("ensure image: %w", err)
	}
	art.Meta["image"] = img.Ref()
	logf("image %s", img.Ref())

	launch := sandbox.LaunchSpec{
		CPU:         2,
		MemLimitMiB: 4096,
		Timeout:     sc.Timeout() + 5*time.Minute, // headroom for setup and collection
		Workdir:     lay.WorkDir,
		Secrets:     inlineSecrets(opts.Creds),
		SecretNames: providerSecrets(opts.Creds),
		BlockEgress: sc.BlockEgress,
		EgressAllowDomains: []string{
			// Only what the agent needs. Anything Beacon itself tried to reach would fail
			// here, which is how the local-only posture gets asserted rather than assumed.
			"api.anthropic.com", "statsig.anthropic.com", "*.anthropic.com",
		},
	}
	if sc.NeedsRealSystemd() {
		// The VM lane has a real kernel and cgroup2, which dockerd needs; the default lane does
		// not. Neither lane makes our entrypoint PID 1, hence the nested container -- see
		// startNested.
		launch.Lane = sandbox.LaneVM
		launch.CPU, launch.MemLimitMiB = 4, 8192 // dockerd + systemd + collector + the agent
		launch.Timeout = sc.Timeout() + 20*time.Minute
		// The allowlist is deliberately not widened. Building the nested image from the sandbox's
		// own filesystem needs no network, so these scenarios assert exactly the same egress
		// posture as every other one -- which is worth more than the convenience of a registry
		// pull would have been.
		art.Meta["lane"] = string(launch.Lane)
	}
	inst, err := p.Launch(ctx, img, launch)
	if err != nil {
		return art, fmt.Errorf("launch: %w", err)
	}
	art.Meta["instance"] = inst.ID()
	logf("sandbox %s", inst.ID())
	if !opts.KeepInstance {
		defer p.Terminate(ctx, inst)
	} else {
		logf("--keep-sandbox set: instance will be left running")
	}

	agent := sandbox.ExecOpts{
		User: lay.AgentUser, Dir: lay.WorkDir, HomeDir: lay.AgentHome,
		PreserveEnv: true, PathPrepend: lay.PathPrepend, Timeout: 5 * time.Minute,
	}
	// privileged is the same options with whatever elevation this platform expresses. On POSIX
	// that is the root account; on Windows there is no account to switch to -- the runner is
	// already an administrator with UAC disabled, and the local backend cannot become anyone
	// else. Naming the difference once keeps every later call site single-path.
	privileged := sandbox.ExecOpts{
		User: lay.PrivilegedUser(), Dir: lay.WorkDir, HomeDir: lay.AgentHome,
		PreserveEnv: true, PathPrepend: lay.PathPrepend, Timeout: 5 * time.Minute,
	}

	// Finish the artifact layer: the snapshot captured the files, but chmod and symlinks have to
	// happen in a live instance. Skipped when nothing was pushed, which is the local backend's
	// case -- the CI job placed the binaries before this process started, so there is no staged
	// tree to fix up.
	if len(imgSpec.Files) > 0 {
		if _, err := p.Exec(ctx, inst, image.PostPushLayers(),
			sandbox.ExecOpts{User: lay.PrivilegedUser(), Timeout: time.Minute}); err != nil {
			return art, fmt.Errorf("prepare binaries: %w", err)
		}
	}

	// Everything the scenario itself does happens in the guest. For most scenarios that is the
	// sandbox; a scenario needing a real systemd gets a nested container instead. See guest.go.
	var g guest = directGuest{p: p, inst: inst}
	if sc.NeedsRealSystemd() {
		ng, err := startNested(ctx, p, inst, logf)
		if err != nil {
			return art, err
		}
		g = ng
		art.Meta["systemd_state"] = ng.systemdState
		art.Meta["docker_version"] = ng.dockerVersion
	}
	art.Meta["guest"] = g.Describe()

	// Not just recorded -- checked. An empty version line means beacon or claude is not reachable
	// in the guest, and every later failure is then a symptom of that rather than of Beacon. The
	// first nested run printed "versions:" with nothing after it and kept going.
	v, err := g.Exec(ctx, versionProbe(p.Platform()), agent)
	if err != nil {
		return art, fmt.Errorf("could not run commands in the %s guest: %w", g.Describe(), err)
	}
	art.Meta["versions"] = oneLine(v.Stdout)
	logf("versions: %s", art.Meta["versions"])
	if !strings.Contains(v.Stdout, "beacon version") {
		return art, fmt.Errorf("the guest did not report a Beacon version, so the binary under "+
			"test is not reachable there (rc=%d): %s", v.ExitCode, oneLine(v.Stdout+v.Stderr))
	}

	sentinel := scenario.Expand(sc.Sentinel, canary, "", lay.WorkDir)
	if sc.Setup != "" {
		setup := scenario.Expand(sc.Setup, canary, sentinel, lay.WorkDir)
		if r, err := g.Exec(ctx, setup, agent); err != nil || r.ExitCode != 0 {
			return art, fmt.Errorf("scenario setup failed (rc=%d): %v %s", r.ExitCode, err, oneLine(r.Stderr))
		}
	}

	logPath := sh.Join(lay.WorkDir, "runtime.jsonl")
	prompt := scenario.Expand(sc.Prompt, canary, sentinel, lay.WorkDir)

	// Start the credential sampler BEFORE the session, so it observes the agent's own
	// processes while they are alive. See shell.ArgvSampler.
	if _, err := p.Exec(ctx, inst, sh.ArgvSampler(), sandbox.ExecOpts{
		User: lay.PrivilegedUser(), Timeout: time.Minute,
	}); err != nil {
		logf("argv sampler failed to start: %v", err)
	}

	// Two ways to get a collector, and which one a scenario uses is the whole point of the
	// distinction. `beacon ci exec` wraps the session in an ephemeral collector -- a real
	// shipping path, used in GitHub Actions and cloud agent sandboxes, needing no service
	// manager. A persistent install instead exercises `beacon endpoint install`: service
	// manager selection, unit installation, status reporting, and whether an installed
	// collector actually receives telemetry. Nothing else in the suite can tell you that.
	sessionDir := workDirFor(sc, lay, sh)
	sessionOpts := sandbox.ExecOpts{
		User: lay.AgentUser, Dir: sessionDir, HomeDir: lay.AgentHome,
		PreserveEnv: true, PathPrepend: lay.PathPrepend, Timeout: sc.Timeout() + time.Minute,
	}
	var res sandbox.Result
	if sc.Install != nil {
		installedLog, err := doInstall(ctx, g, sc, privileged, agent, lay, sh, &art, logf)
		// Uninstall afterwards when the sandbox is this machine, whether or not the install
		// succeeded -- a partial install still holds the OTLP ports.
		//
		// A disposable per-scenario instance makes this unnecessary: Terminate throws the machine
		// away and the next scenario starts clean. LocalExec has no machine to throw away, so an
		// installed endpoint outlives the scenario that installed it, and the next install fails on
		// "OTLP gRPC port 4317 and HTTP port 4318 are already in use". That is what happened: a
		// system-mode scenario left its collector running and the user-mode scenario after it could
		// not bind, so the suite's result depended on the order its scenarios happened to run in.
		//
		// Deferred rather than run inline because artifact collection happens further down this
		// function and needs the log to still be there; --keep-logs is what makes that safe.
		if local && !opts.KeepInstance {
			defer teardownInstall(ctx, g, sc, privileged, logf)
		}
		if err != nil {
			return art, err
		}
		logPath = installedLog
		logf("running session against the installed endpoint (budget $%.2f, timeout %s)",
			sc.Budget(), sc.Timeout())
		res, err = g.Exec(ctx, sh.PlainSession(sessionDir, claudeFlags(sc, prompt, sh)), sessionOpts)
		if err != nil {
			logf("session exec error: %v", err)
		}
		art.Meta["session"] = oneLine(res.Stdout)
	} else {
		logf("running session (budget $%.2f, timeout %s)", sc.Budget(), sc.Timeout())
		var err error
		res, err = g.Exec(ctx, sh.CIExecSession(logPath, sessionDir, claudeFlags(sc, prompt, sh)), sessionOpts)
		if err != nil {
			logf("session exec error: %v", err)
		}
		art.Meta["ci_exec"] = oneLine(res.Stdout)
		// Recorded explicitly so a failing collector is visible in the run metadata rather
		// than inferred from a missing log later.
		art.Meta["ci_exec_rc"] = fmt.Sprintf("%d", res.ExitCode)
		if res.ExitCode != 0 {
			logf("beacon ci exec exited %d -- the collected log may be incomplete", res.ExitCode)
			// Written whole, not folded into meta. meta values pass through oneLine, which caps
			// them at 400 characters -- enough to show the middle of a stderr tail and cut off the
			// error at its end, which is the only part worth having. A failing collector is exactly
			// when the full text is needed, so it goes to the run directory intact.
			if err := os.WriteFile(filepath.Join(runDir, "ci-exec-output.txt"),
				[]byte(res.Stdout+"\n--- stderr ---\n"+res.Stderr), 0o600); err == nil {
				logf("wrote the full ci exec output to ci-exec-output.txt")
			}
		}
	}

	// Claude's own result JSON tells us whether the session itself succeeded, which
	// separates an agent/auth failure from a capture failure.
	//
	// Read by absolute path. The session runs in the scenario's cwd, so a relative read from
	// the default working directory silently found nothing whenever `cwd:` was set -- s05 was
	// the only such scenario and it reported session_ok=false on an otherwise perfect run,
	// disabling exactly the signal that distinguishes a dead agent from a capture gap.
	outPath := sessionFile(sc, "claude-out.json", lay, sh)
	errPath := sessionFile(sc, "claude-err.txt", lay, sh)
	if r, err := g.Exec(ctx, sh.ReadFile(outPath), agent); err == nil {
		var result map[string]any
		if json.Unmarshal([]byte(strings.TrimSpace(r.Stdout)), &result) == nil {
			art.SessionOK, art.SessionKnown = sessionOutcome(result)
			if !art.SessionKnown {
				logf("claude result has no boolean is_error; session outcome is unknown")
			}
			art.Meta["claude_result"] = fmt.Sprintf("subtype=%v is_error=%v turns=%v cost=%v session=%v",
				result["subtype"], result["is_error"], result["num_turns"],
				result["total_cost_usd"], result["session_id"])
			logf("claude: %s", art.Meta["claude_result"])
			_ = os.WriteFile(filepath.Join(runDir, "claude-result.json"), []byte(r.Stdout), 0o600)
		} else {
			if e, err2 := g.Exec(ctx, sh.TailBytes(errPath, 800), agent); err2 == nil {
				art.Meta["claude_stderr"] = oneLine(e.Stdout)
			}
		}
	}

	if sc.Sentinel != "" {
		// The exec error is not discarded. On a failed guest exec stdout is empty, so
		// __MISSING__ never appears and the probe silently concluded the file existed -- making
		// an infrastructure failure indistinguishable from "the agent skipped the work", which
		// the verdict then reported as a retry-worthy INCONCLUSIVE. The argv path below already
		// required a successful exec before trusting its output; this one now does too.
		// Reported by Cursor Bugbot.
		// The probe always emits a marker, so its output is never ambiguous. `cat f || echo
		// __MISSING__` produced empty stdout for both a failed exec and a legitimately empty
		// sentinel file, which would have misreported the second as a broken probe.
		r, execErr := g.Exec(ctx, sh.ProbeFile(sentinel), agent)
		art.SentinelProbed = execErr == nil &&
			(strings.Contains(r.Stdout, "__FOUND__") || strings.Contains(r.Stdout, "__MISSING__"))
		if !art.SentinelProbed {
			logf("sentinel probe failed (%v): whether the agent acted is unknown for this run", execErr)
		}
		// The marker is plumbing, not content: it appears in the verdict as
		// "confirmed: <detail>", so leaving it in leaks an implementation detail into output a
		// human reads.
		art.SentinelDetail = strings.TrimSpace(strings.NewReplacer(
			"__FOUND__", "", "__MISSING__", "not found").Replace(r.Stdout))
		// Existence alone is not proof the agent acted. s05's sentinel was the seeded
		// README.md and s04's was copied into place by setup, so both were present no matter
		// what the agent did -- INCONCLUSIVE became unreachable and an idle agent produced a
		// capture FAIL instead. Requiring the run-unique canary makes the sentinel mean what
		// it claims, since only this run's agent could have put it there.
		exists := strings.Contains(r.Stdout, "__FOUND__")
		art.SentinelPresent = art.SentinelProbed && exists && strings.Contains(r.Stdout, canary)
		if exists && !art.SentinelPresent {
			logf("sentinel exists but does not contain the canary: the agent did not write it")
		}
		logf("sentinel present=%v", art.SentinelPresent)
	}

	// Read what the in-session sampler observed while the agent was actually running. See
	// argvSamplerScript for why this cannot be a post-session scan.
	if r, err := p.Exec(ctx, inst, sh.ReadFile(sh.ArgvVerdictPath()),
		sandbox.ExecOpts{User: lay.PrivilegedUser(), Timeout: time.Minute}); err == nil {
		sawAgent := strings.Contains(r.Stdout, "saw_agent=1")
		art.Meta["argv_saw_agent"] = fmt.Sprintf("%v", sawAgent)
		switch {
		case strings.Contains(r.Stdout, "ARGV_LEAK"):
			// A leak is a leak regardless: the sampler found the key on a command line, which is
			// positive evidence and needs no corroboration.
			art.SecretInArgv, art.ArgvCheckRan = true, true
			art.Meta["argv_leak_detail"] = oneLine(r.Stdout)
			logf("argv leak detail: %s", art.Meta["argv_leak_detail"])
		case strings.Contains(r.Stdout, "ARGV_CLEAN") && sawAgent:
			art.SecretInArgv, art.ArgvCheckRan = false, true
		case strings.Contains(r.Stdout, "ARGV_CLEAN"):
			// Clean, but the sampler never had the agent in view -- so it searched the wrong
			// processes and proves nothing. Reported unverified rather than clean, which is the
			// whole point of tracking whether a check could run separately from what it found.
			art.ArgvCheckRan = false
			logf("argv scan found nothing, but never observed an agent process: " +
				"the result is unverified, not clean")
		default:
			// The sampler never wrote a verdict, so nothing was observed and the result
			// proves nothing. Reported as unverified rather than clean.
			art.ArgvCheckRan = false
		}
		art.Meta["argv_samples"] = oneLine(sampleCountOf(r.Stdout))
		logf("argv scan: ran=%v leak=%v samples=%s",
			art.ArgvCheckRan, art.SecretInArgv, art.Meta["argv_samples"])
	}
	art.Meta["secret_in_argv"] = fmt.Sprintf("%v", art.SecretInArgv)
	art.Meta["argv_check_ran"] = fmt.Sprintf("%v", art.ArgvCheckRan)

	// Persist the run-scoped facts the offline `verify` path cannot re-derive.
	art.Meta["credential_source"] = string(opts.Creds.Source)
	// Recorded so an offline re-judge can say which secret it could not search for, instead of
	// falling back to whatever key happens to be in the environment at that later time.
	if opts.Creds.ProviderSecretName != "" {
		art.Meta["credential_secret_name"] = opts.Creds.ProviderSecretName
	}
	// A non-reversible fingerprint, never the value. It lets an offline re-judge tell whether
	// the key it has is the one this run used; searching for a different key finds nothing and
	// would report clean while a real disclosure of the original went unnoticed.
	if fp := credentials.Fingerprint(opts.Creds.Value); fp != "" {
		art.Meta["credential_fingerprint"] = fp
	}
	art.Meta["canary"] = canary
	art.Meta["sentinel_present"] = fmt.Sprintf("%v", art.SentinelPresent)
	art.Meta["sentinel_probed"] = fmt.Sprintf("%v", art.SentinelProbed)
	art.Meta["sentinel_detail"] = oneLine(art.SentinelDetail)
	art.Meta["session_ok"] = fmt.Sprintf("%v", art.SessionOK)
	art.Meta["session_known"] = fmt.Sprintf("%v", art.SessionKnown)

	// Wait for quiescence rather than sleeping a fixed amount: the collector batches at 5s
	// and Claude Code's metric interval defaults to 60s, so a fixed sleep is either wasteful
	// or wrong. Record how long it took -- that latency is itself a finding.
	stable, waited := waitForQuiescence(ctx, g, agent, sh, logPath, logf)
	art.Meta["quiescence_seconds"] = fmt.Sprintf("%.0f", waited.Seconds())
	art.Meta["quiescent"] = fmt.Sprintf("%v", stable)

	// Measured after quiescence, so the collector is resident and idle rather than mid-batch.
	// Only for install scenarios: a `ci exec` collector is torn down with the command and has no
	// steady state to describe. Recorded rather than asserted -- these are the numbers a customer
	// evaluating footprint asks for, and publishing an estimate instead of a measurement is how a
	// resource claim quietly stops being true.
	if sc.Install != nil {
		if snap := collectorFootprint(ctx, g, agent); snap != "" {
			art.Meta["collector_footprint"] = snap
			logf("collector footprint: %s", snap)
		}
	}

	localLog := filepath.Join(runDir, "runtime.jsonl")
	if err := g.Get(ctx, logPath, localLog); err != nil {
		return art, fmt.Errorf("collect runtime log: %w", err)
	}
	art.RuntimeLog = localLog
	logf("collected %s", localLog)

	// Reinstall before uninstall, for the same reason uninstall runs here at all: the log is already
	// on the host, so restarting the collector cannot disturb a capture assertion.
	if sc.VerifiesRestartOnReinstall() {
		probeReinstall(ctx, g, sc, privileged, agent, &art, logf)
	}

	// Unprivileged first, while there is still something installed for it to fail to remove.
	if sc.VerifiesUnprivilegedUninstallFails() {
		probeUnprivilegedUninstall(ctx, g, agent, &art, logf)
	}

	// Uninstall last, and only when the scenario asked. The runtime log is already on the host, so
	// removing the endpoint cannot affect any capture assertion -- which is what makes it safe to do
	// inside the same run rather than needing a scenario of its own.
	if sc.Install != nil && sc.Install.VerifyUninstall {
		probeUninstall(ctx, g, sc, privileged, agent, lay, sh, &art, logf)
	}

	// meta.json is written by the deferred host-state block above, so it always includes the
	// host comparison.
	return art, nil
}

// sessionOutcome reads the agent's own success report without inventing information.
//
// Only a present boolean is evidence. result["is_error"] is nil when the key is absent, and in Go
// `nil == false` evaluates to false, so the obvious form recorded a *known failure* for any result
// JSON lacking the field -- the same invented-failure shape decodeSession was written to avoid on
// the offline side, reproduced here in the capture path. Reported by Cursor Bugbot.
func sessionOutcome(result map[string]any) (ok, known bool) {
	isErr, present := result["is_error"].(bool)
	if !present {
		return false, false
	}
	return !isErr, true
}

// versionProbe asks the guest to identify itself.
//
// Not just recorded but checked by the caller: an empty version line means beacon or claude is
// not reachable in the guest, and every later failure is then a symptom of that rather than of
// Beacon.
func versionProbe(p sandbox.Platform) string {
	if p.IsWindows() {
		return "& beacon version; & claude --version; " +
			"[System.Security.Principal.WindowsIdentity]::GetCurrent().Name; $env:PROCESSOR_ARCHITECTURE"
	}
	return "beacon version; claude --version; id -un; uname -m"
}

// sessionFile resolves a file the agent session wrote, relative to the directory that session
// actually ran in.
func sessionFile(sc scenario.Scenario, name string, lay image.Layout, sh shell) string {
	return sh.Join(workDirFor(sc, lay, sh), name)
}

// claudeFlags builds the agent invocation shared by both session shapes.
func claudeFlags(sc scenario.Scenario, prompt string, sh shell) []string {
	flags := []string{
		"-p", sh.Quote(prompt),
		"--output-format", "json",
		"--dangerously-skip-permissions",
		fmt.Sprintf("--max-budget-usd %.2f", sc.Budget()),
	}
	if len(sc.Tools) > 0 {
		flags = append(flags, "--tools", sh.Quote(strings.Join(sc.Tools, ",")))
	} else if sc.Tools != nil {
		flags = append(flags, "--tools", sh.Quote(""))
	}
	if len(sc.DisallowedTools) > 0 {
		flags = append(flags, "--disallowedTools", sh.Quote(strings.Join(sc.DisallowedTools, ",")))
	}
	return flags
}

// waitForQuiescence polls the log until its size stops changing, bounded.
func waitForQuiescence(ctx context.Context, g guest,
	opts sandbox.ExecOpts, sh shell, logPath string, logf func(string, ...any)) (bool, time.Duration) {

	const (
		interval  = 3 * time.Second
		stableFor = 9 * time.Second
		maxWait   = 90 * time.Second
	)
	start := time.Now()
	var last string
	stableSince := time.Time{}

	for time.Since(start) < maxWait {
		r, err := g.Exec(ctx, sh.FileSize(logPath), opts)
		if err == nil {
			cur := strings.TrimSpace(r.Stdout)
			if cur == last && cur != "0" {
				if stableSince.IsZero() {
					stableSince = time.Now()
				} else if time.Since(stableSince) >= stableFor {
					return true, time.Since(start)
				}
			} else {
				stableSince = time.Time{}
				last = cur
			}
		}
		select {
		case <-ctx.Done():
			return false, time.Since(start)
		case <-time.After(interval):
		}
	}
	logf("log never went quiet within %s; collecting anyway", maxWait)
	return false, time.Since(start)
}

func workDirFor(sc scenario.Scenario, lay image.Layout, sh shell) string {
	if sc.Cwd == "" {
		return lay.WorkDir
	}
	return sh.Join(lay.WorkDir, sc.Cwd)
}

// imageSpecFor describes the environment a scenario needs.
//
// Windows takes the short path deliberately: nothing here provisions it. The CI job built the
// binaries, installed the agent runtime and created the work directory before this process
// started, so there is no image to build and no artifact to push. What would otherwise be
// checked here -- that the binary under test is actually reachable -- is checked better a few
// lines later by asking the guest for `beacon version`, which tests reachability rather than a
// path guess.
func imageSpecFor(p sandbox.Platform, sc scenario.Scenario, opts Options,
	logf func(string, ...any)) (sandbox.ImageSpec, error) {

	if p.IsWindows() {
		return sandbox.ImageSpec{Base: "windows/amd64"}, nil
	}
	logf("building image (cached layers are free)")
	return image.Build(image.Spec{
		RepoRoot:      opts.RepoRoot,
		ClaudeVersion: opts.ClaudeVersion,
		WithDocker:    sc.NeedsRealSystemd(),
		NSSOnlyUser:   sc.NeedsNSSOnlyUser(),
	}, logf)
}

// collectorFootprint measures the resident collector: memory, threads, descriptors, and the CPU it
// consumes while idle.
//
// Read from /proc rather than from ps output formats that differ between distributions, and
// sampled over a fixed interval because idle CPU is a rate, not a value -- the total ticks a
// process has ever used says nothing about what it costs to leave running.
//
// Best-effort by design: this describes a run, it does not gate one. A shell that cannot produce
// the numbers returns an empty string and the scenario proceeds, because a footprint measurement
// failing is not a reason to fail a capture test.
func collectorFootprint(ctx context.Context, g guest, opts sandbox.ExecOpts) string {
	// Sampled over 30s rather than 10s: at 100 ticks/second an idle collector accumulates about
	// one tick per 10s, so a 10s window reports 0 or 1 and cannot distinguish "almost nothing"
	// from "nothing". 30s buys a figure with more than one significant digit.
	//
	// The descriptor count needs sudo. In system mode the collector runs as root while this shell
	// runs as the agent user, so /proc/<pid>/fd is not readable and a plain `ls` yields zero --
	// a number that is not merely imprecise but impossible, since every process holds at least
	// stdin, stdout, and stderr. "unreadable" is reported instead, because publishing a resource
	// figure that is obviously false costs more credibility than omitting one.
	const script = `
pid=$(pgrep -n beacon-otelcol 2>/dev/null) || exit 0
[ -n "$pid" ] || exit 0
read_ticks() { awk '{print $14+$15}' /proc/$1/stat 2>/dev/null; }
t0=$(read_ticks "$pid"); sleep 30; t1=$(read_ticks "$pid")
hz=$(getconf CLK_TCK 2>/dev/null || echo 100)
rss=$(awk '/^VmRSS:/{print $2}' /proc/$pid/status 2>/dev/null)
thr=$(awk '/^Threads:/{print $2}' /proc/$pid/status 2>/dev/null)
fds=$(sudo ls /proc/$pid/fd 2>/dev/null | wc -l | tr -d ' ')
[ "${fds:-0}" -gt 0 ] 2>/dev/null || fds=unreadable
echo "rss_kb=${rss:-?} threads=${thr:-?} fds=${fds} cpu_ticks_per_30s=$((t1-t0)) clk_tck=$hz"
`
	res, err := g.Exec(ctx, script, opts)
	if err != nil || res.ExitCode != 0 {
		return ""
	}
	return oneLine(res.Stdout)
}

func randomToken() string {
	b := make([]byte, 6)
	if _, err := rand.Read(b); err != nil {
		return fmt.Sprintf("%d", time.Now().UnixNano())
	}
	return hex.EncodeToString(b)
}

func shq(s string) string { return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'" }

func oneLine(s string) string {
	s = strings.Join(strings.Fields(strings.ReplaceAll(s, "\n", " ")), " ")
	if len(s) > 400 {
		return s[:400] + "..."
	}
	return s
}

// inlineSecrets returns the key/value secrets to inject, empty when the provider holds the
// credential itself.
func inlineSecrets(c credentials.Resolved) map[string]string {
	if c.Value == "" {
		return nil
	}
	return map[string]string{credentials.EnvVar: c.Value}
}

// providerSecrets returns provider-stored secret names to attach.
func providerSecrets(c credentials.Resolved) []string {
	if c.ProviderSecretName == "" {
		return nil
	}
	return []string{c.ProviderSecretName}
}

// argvVerdictPath is where the in-guest sampler records what it saw.
const argvVerdictPath = "/tmp/beacon-sandbox-argv-verdict"

// argvSamplerScript starts a background sampler that watches for the injected credential in the
// guest process table for as long as the agent session runs.
//
// It has to run concurrently with the session, and an earlier version did not: the scan executed
// after `beacon ci exec` had already returned, so every process that might have held the key in
// argv was already gone and ARGV_CLEAN was close to vacuous -- a disclosure claim resting on an
// observation never made. Flagged by Cursor Bugbot on the first commit of this tool.
//
// Both `ps` output and every /proc/<pid>/cmdline are checked, since a shell can hide argv from
// one but not the other. It runs as root because another user's cmdline is otherwise unreadable,
// and a check that can only see its own process proves nothing.
//
// The matcher must not carry the key in its own argv or the scan finds itself: a first version
// used `grep -qF "$KEY"`, which the shell expands into grep's argv, so the `ps` in the same
// pipeline saw the grep process and reported a leak the tool had created. awk reads the key from
// ENVIRON instead, and the sampler's own processes are skipped as a second guard.
//
// The body is delivered through a quoted heredoc so the guest shell performs no expansion on it,
// which keeps the awk program readable instead of buried under layers of escaping.
func argvSamplerScript() string {
	return fmt.Sprintf(`set -u
rm -f %[1]s
if [ -z "${ANTHROPIC_API_KEY:-}" ]; then
  echo "ARGV_CHECK_INVALID_NO_KEY samples=0" > %[1]s
  exit 0
fi
cat > %[2]s <<'SAMPLER'
#!/bin/sh
VERDICT="$1"
scan() {
  awk 'BEGIN{k=ENVIRON["ANTHROPIC_API_KEY"]; if (k=="") exit 1}
       /awk|ARGV_SAMPLER_SELF/ {next}
       index($0,k){found=1}
       END{exit !found}'
}
n=0
saw_agent=0
# Bounded so a leaked sandbox cannot spin forever; the instance is destroyed long before this.
while [ "$n" -lt 900 ]; do
  n=$((n+1))
  procs="$(ps -eo args= 2>/dev/null || true)"
  # Whether the agent was ever in view at all. A clean verdict from a sampler that never saw the
  # process holding the key is not evidence of anything -- and in the nested lane, where the agent
  # runs in a child PID namespace, "the parent namespace sees its children" is an assumption worth
  # confirming rather than trusting.
  case "$procs" in *claude*) saw_agent=1 ;; esac
  if printf '%%s\n' "$procs" | scan; then
    echo "ARGV_LEAK samples=$n saw_agent=$saw_agent via=ps" > "$VERDICT"
    exit 0
  fi
  for f in /proc/[0-9]*/cmdline; do
    [ -r "$f" ] || continue
    cmd="$(tr '\0' '\n' < "$f" 2>/dev/null || true)"
    case "$cmd" in *claude*) saw_agent=1 ;; esac
    if printf '%%s\n' "$cmd" | scan; then
      echo "ARGV_LEAK samples=$n saw_agent=$saw_agent via=proc" > "$VERDICT"
      exit 0
    fi
  done
  echo "ARGV_CLEAN samples=$n saw_agent=$saw_agent" > "$VERDICT"
  sleep 1
done
SAMPLER
chmod +x %[2]s
nohup %[2]s %[1]s > /dev/null 2>&1 &
echo ARGV_SAMPLER_STARTED`, argvVerdictPath, "/tmp/beacon-sandbox-argv-sampler.sh")
}

// sampleCountOf extracts the sampler's iteration count for the run metadata.
//
// A clean result backed by one sample is much weaker evidence than one backed by hundreds, so
// the number is recorded rather than hidden behind a boolean. Observed density is modest -- a
// short session typically yields single-digit samples, because each pass forks `tr` once per
// process in /proc -- so `secret_in_argv=false` means "not seen in N samples spread across the
// session", not "provably never present". Read argv_samples alongside it.
func sampleCountOf(verdict string) string {
	for _, f := range strings.Fields(verdict) {
		if c, ok := strings.CutPrefix(f, "samples="); ok {
			return c
		}
	}
	return "0"
}

// doInstall performs a persistent `beacon endpoint install` and returns the runtime log the
// installed collector writes to.
//
// The log path comes from `endpoint status --json` rather than being assumed, because the whole
// point of an install scenario is to check what Beacon actually did — hardcoding the expected
// path would make the scenario agree with itself instead of with Beacon.
func doInstall(ctx context.Context, g guest, sc scenario.Scenario,
	privileged, agent sandbox.ExecOpts, lay image.Layout, sh shell,
	art *Artifacts, logf func(string, ...any)) (string, error) {

	in := sc.Install
	system := in.Mode == "system"
	scope := "--user"
	if system {
		scope = "--system"
	}
	svc := ""
	if in.Service != "" {
		svc = " --service=" + in.Service
	}
	// System-mode install needs elevation; user mode must not have it, or the install would
	// write system paths while claiming to be a user install. On Windows the two options differ
	// only in what they cannot do -- there is one account, already elevated -- which is a real
	// weakness of the Windows user-mode scenario and is why w02 asserts the supervised backend
	// rather than trusting that it ran unprivileged.
	opts := agent
	if system {
		opts = privileged
	}
	art.Meta["install_mode"] = in.Mode
	art.Meta["install_service"] = in.Service

	// Dry run first. It is free, and it is where a plan that advertises the wrong service
	// manager for the host would show up — so a regression there needs no second scenario.
	if r, err := g.Exec(ctx, "beacon endpoint install "+scope+svc+" --harness claude --dry-run 2>&1", opts); err == nil {
		art.Meta["install_dry_run"] = oneLine(r.Stdout)
		logf("dry-run plan: %s", art.Meta["install_dry_run"])
	}

	r, err := g.Exec(ctx, "beacon endpoint install "+scope+svc+" --harness claude 2>&1", opts)
	art.Meta["install_output"] = oneLine(r.Stdout + r.Stderr)
	if err != nil || r.ExitCode != 0 {
		logf("install FAILED rc=%d: %s", r.ExitCode, art.Meta["install_output"])
		return "", fmt.Errorf("endpoint install failed (rc=%d): %s",
			r.ExitCode, oneLine(r.Stdout+r.Stderr))
	}
	logf("install ok: %s", art.Meta["install_output"])

	// A system install configures the *installing* user's agent runtime, which for a package
	// install is root -- not the person whose sessions matter. The package postinstall therefore
	// runs a second step to resolve that person and point their Claude Code at the collector, and
	// a system-mode scenario has to do the same or its capture assertions would be testing
	// nothing but root's unused settings file.
	//
	// SUDO_USER is how the real path identifies them (`sudo apt install ./beacon.deb`), so it is
	// what gets set here rather than inventing a flag that no user would pass.
	if system {
		cmd := sh.WithEnv("SUDO_USER", lay.ConsoleUser(),
			"beacon endpoint user-config repair-installed --system --harness claude --json 2>&1")
		u, err := g.Exec(ctx, cmd, opts)
		art.Meta["install_user_config"] = oneLine(u.Stdout + u.Stderr)
		if err != nil || u.ExitCode != 0 {
			return "", fmt.Errorf("configuring %s's agent runtime failed (rc=%d): %v %s",
				lay.ConsoleUser(), u.ExitCode, err, art.Meta["install_user_config"])
		}
		logf("user config: %s", art.Meta["install_user_config"])
		// A skip is not a pass. The command reports one rather than failing, because on a real
		// unattended host there may be nobody to configure -- but in this scenario there is, and
		// treating a skip as success would let a broken resolver produce a green run.
		if strings.Contains(art.Meta["install_user_config"], "skipped_reason") {
			return "", fmt.Errorf("user-config skipped instead of configuring %s: %s",
				lay.ConsoleUser(), art.Meta["install_user_config"])
		}
	}

	logPath, err := installedLogPath(ctx, g, scope, opts, in, lay, art, logf)
	if err != nil {
		return "", err
	}
	// Doctor output is recorded whether or not it passes: on a failure it is the first thing
	// worth reading, and it costs one exec.
	if d, err := g.Exec(ctx, "beacon endpoint doctor "+scope+" --json 2>&1", opts); err == nil {
		art.Meta["install_doctor"] = oneLine(d.Stdout)
	}
	return logPath, nil
}

// installedLogPath reads `endpoint status --json` for the service state and log path.
//
// A status that cannot be parsed is not fatal — the run can still proceed against the
// documented default path — but ExpectStatusRunning is enforced, since a scenario asking for a
// running service and silently accepting an unparseable status would verify nothing.
func installedLogPath(ctx context.Context, g guest,
	scope string, opts sandbox.ExecOpts, in *scenario.Install, lay image.Layout, art *Artifacts,
	logf func(string, ...any)) (string, error) {

	s, err := g.Exec(ctx, "beacon endpoint status "+scope+" --json 2>&1", opts)
	if err == nil {
		art.Meta["install_status"] = oneLine(s.Stdout)
		var status struct {
			LogPath    string `json:"log_path"`
			ConfigPath string `json:"config_path"`
			Service    struct {
				Label   string `json:"label"`
				Loaded  bool   `json:"loaded"`
				Running bool   `json:"running"`
				Kind    string `json:"kind"`
				Message string `json:"message"`
			} `json:"service"`
		}
		if json.Unmarshal([]byte(s.Stdout), &status) == nil {
			// Recorded so a later uninstall probe checks the paths Beacon actually used rather than
			// the ones a scenario assumed.
			art.Meta["install_log_path"] = status.LogPath
			art.Meta["install_config_path"] = status.ConfigPath
			art.Meta["service_kind"] = status.Service.Kind
			art.Meta["service_label"] = status.Service.Label
			art.Meta["service_loaded"] = fmt.Sprintf("%v", status.Service.Loaded)
			art.Meta["service_running"] = fmt.Sprintf("%v", status.Service.Running)
			logf("service: kind=%s label=%s loaded=%v running=%v %s",
				status.Service.Kind, status.Service.Label, status.Service.Loaded,
				status.Service.Running, status.Service.Message)
			if in.ExpectStatusRunning && !status.Service.Running {
				return "", fmt.Errorf("service did not report running after install: %s",
					status.Service.Message)
			}
			if in.ExpectServiceKind != "" && status.Service.Kind != in.ExpectServiceKind {
				return "", fmt.Errorf("service backend is %q, want %q: the scenario would "+
					"otherwise verify a different service manager than it claims to",
					status.Service.Kind, in.ExpectServiceKind)
			}
			if status.LogPath != "" {
				return status.LogPath, nil
			}
		}
	}
	if in.ExpectStatusRunning || in.ExpectServiceKind != "" {
		return "", fmt.Errorf("could not read service status, so neither a running service nor " +
			"the selected backend can be confirmed; refusing to report a pass the run did not earn")
	}
	logf("status unparseable; falling back to the documented default log path")
	return lay.DefaultLogPath(in.Mode == "system"), nil
}

// teardownInstall removes an endpoint the scenario installed, so the next scenario on this machine
// starts from the same state the first one did.
//
// Only reached when the provider mutates the host. Everywhere else the instance is discarded, which
// is a stronger guarantee than any uninstall: it also takes back whatever the install touched that
// uninstall does not know about.
//
// Failures are logged rather than returned. This runs after the scenario has been judged, so turning
// a cleanup problem into a scenario failure would report the wrong thing -- but staying silent would
// leave the *next* scenario to fail for a reason that has nothing to do with it, which is exactly the
// confusion this function exists to prevent. Naming it here is what makes that traceable.
func teardownInstall(ctx context.Context, g guest, sc scenario.Scenario,
	privileged sandbox.ExecOpts, logf func(string, ...any)) {

	scope := "--user"
	if sc.Install != nil && sc.Install.Mode == "system" {
		scope = "--system"
	}
	// The log is deliberately *not* kept.
	//
	// Collection happens inline, before any deferred function runs, so this cannot take away an
	// artifact the run still needs. Keeping it does active harm: every system-mode scenario installs
	// to the same %ProgramData% path and the collector appends, so a kept log carries the previous
	// scenario's events into the next one's verdict. Three scenarios in one suite reported 38, then
	// 87, then 129 events -- 38 + 49 is 87, which is the accumulation showing through rather than a
	// scenario capturing more.
	//
	// An expectation scoped by the run's canary survives that; one that only requires a field to be
	// present does not, and would pass on a leftover from the scenario before. Removing the log is
	// what makes each scenario judge its own run.
	res, err := g.Exec(ctx, "beacon endpoint uninstall "+scope+" 2>&1", privileged)
	switch {
	case err != nil:
		logf("teardown: uninstall %s did not run: %v", scope, err)
	case res.ExitCode != 0:
		logf("teardown: uninstall %s exited %d: %s", scope, res.ExitCode, oneLine(res.Stdout))
	default:
		logf("teardown: uninstalled the %s endpoint", strings.TrimPrefix(scope, "--"))
	}
}

// probeUninstall removes the endpoint and records what survived.
//
// Deliberately records rather than judges. Every other verdict in this tool is produced by the pure
// check package from collected artifacts, which is what makes `verify` able to re-judge a saved run
// for free -- so this writes observations into meta and leaves the conclusions to check.Uninstall.
//
// The observations are gathered by asking the operating system, not by asking Beacon. `endpoint
// status` reporting "not installed" is Beacon's opinion of its own state; whether the service is
// still registered is a fact about the machine, and those are exactly the two that came apart in the
// bug this exists to catch.
// probeReinstall installs a second time and checks the collector process was replaced.
//
// Runs after the runtime log has been collected, so restarting the collector cannot affect any
// capture assertion -- the same reason probeUninstall runs where it does.
//
// The pid is the evidence. Asking the service whether it is "running" proves nothing here: the
// stale collector is running, which is precisely the bug. Only a changed pid distinguishes "the
// install restarted it" from "the install found it alive and left it there".
func probeReinstall(ctx context.Context, g guest, sc scenario.Scenario,
	privileged, agent sandbox.ExecOpts, art *Artifacts, logf func(string, ...any)) {

	scope := "--user"
	opts := agent
	if sc.Install.Mode == "system" {
		scope = "--system"
		opts = privileged
	}

	before, _ := g.Exec(ctx, "pgrep -n beacon-otelcol", opts)
	pidBefore := strings.TrimSpace(before.Stdout)
	if pidBefore == "" {
		art.Meta["reinstall_error"] = "no collector was running before the reinstall, so a restart cannot be observed"
		logf("reinstall probe: %s", art.Meta["reinstall_error"])
		return
	}

	args := "beacon endpoint install " + scope + " --harness claude"
	if svc := sc.Install.Service; svc != "" {
		args += " --service=" + svc
	}
	r, err := g.Exec(ctx, args+" 2>&1", opts)
	art.Meta["reinstall_rc"] = fmt.Sprintf("%d", r.ExitCode)
	art.Meta["reinstall_output"] = oneLine(r.Stdout + r.Stderr)
	if err != nil || r.ExitCode != 0 {
		art.Meta["reinstall_error"] = "the second install failed: " + art.Meta["reinstall_output"]
		logf("reinstall probe: %s", art.Meta["reinstall_error"])
		return
	}

	after, _ := g.Exec(ctx, "pgrep -n beacon-otelcol", opts)
	pidAfter := strings.TrimSpace(after.Stdout)

	art.Meta["reinstall_pid_before"] = pidBefore
	art.Meta["reinstall_pid_after"] = pidAfter
	art.Meta["reinstall_restarted"] = fmt.Sprintf("%v", pidAfter != "" && pidAfter != pidBefore)
	logf("reinstall: collector pid %s -> %s (restarted=%s)", pidBefore, pidAfter, art.Meta["reinstall_restarted"])
}

// probeUnprivilegedUninstall checks that a system removal without privileges reports failure.
//
// Run before the real uninstall, and deliberately as the unprivileged agent user. What makes this
// worth its own probe is that the bug it catches is invisible from the elevated path: the removal
// reported success, exited 0, and left the service registered.
func probeUnprivilegedUninstall(ctx context.Context, g guest, agent sandbox.ExecOpts,
	art *Artifacts, logf func(string, ...any)) {

	r, _ := g.Exec(ctx, "beacon endpoint uninstall --system --keep-logs 2>&1", agent)
	art.Meta["unprivileged_uninstall_rc"] = fmt.Sprintf("%d", r.ExitCode)
	art.Meta["unprivileged_uninstall_output"] = oneLine(r.Stdout + r.Stderr)
	art.Meta["unprivileged_uninstall_refused"] = fmt.Sprintf("%v", r.ExitCode != 0)
	logf("unprivileged uninstall: rc=%d refused=%s", r.ExitCode, art.Meta["unprivileged_uninstall_refused"])
}

func probeUninstall(ctx context.Context, g guest, sc scenario.Scenario,
	privileged, agent sandbox.ExecOpts, lay image.Layout, sh shell,
	art *Artifacts, logf func(string, ...any)) {

	system := sc.Install.Mode == "system"
	scope := "--user"
	opts := agent
	if system {
		scope = "--system"
		opts = privileged
	}

	// The service label and kind were recorded at install time. Without them there is nothing to ask
	// the service manager about, so the probe says so rather than reporting a removal it could not
	// verify.
	label := art.Meta["service_label"]
	kind := art.Meta["service_kind"]

	// The log path comes from the install status, so the probe checks the path Beacon actually used
	// rather than the one the scenario assumed.
	//
	// The endpoint config is deliberately not checked. `endpoint uninstall` removes everything in its
	// install manifest, which includes config.json and otelcol.yaml, and --keep-config does not change
	// that -- it governs *harness* telemetry settings. Retaining Beacon's own config is a contract the
	// packaging layer keeps by stashing those files around a removal, not one the CLI offers, so
	// asserting it here would fail every correct run.
	logPath := art.Meta["install_log_path"]

	// --keep-logs, because keeping collected telemetry through a removal is a real contract: an
	// uninstall is often the first half of a reinstall. Asserting that it held is half of what this
	// probe is for; the other half is that the service actually went.
	r, err := g.Exec(ctx, "beacon endpoint uninstall "+scope+" --keep-logs 2>&1", opts)
	art.Meta["uninstall_ran"] = "true"
	art.Meta["uninstall_output"] = oneLine(r.Stdout + r.Stderr)
	art.Meta["uninstall_rc"] = fmt.Sprintf("%d", r.ExitCode)
	if err != nil {
		art.Meta["uninstall_exec_error"] = oneLine(err.Error())
	}
	logf("uninstall rc=%d: %s", r.ExitCode, art.Meta["uninstall_output"])

	// Asked of the service manager directly. A supervised collector has no manager to ask, so its
	// absence is established from the pidfile instead, and the probe records which question it asked.
	if label != "" && kind != "" && kind != "none" {
		query := sh.ServiceQuery(kind, label)
		if query != "" {
			q, _ := g.Exec(ctx, query, opts)
			art.Meta["uninstall_service_query"] = oneLine(q.Stdout + q.Stderr)
			art.Meta["uninstall_service_query_rc"] = fmt.Sprintf("%d", q.ExitCode)
			art.Meta["uninstall_service_gone"] = fmt.Sprintf("%v", sh.ServiceAbsent(kind, q.ExitCode, q.Stdout+q.Stderr))
			logf("service %s after uninstall: gone=%s", label, art.Meta["uninstall_service_gone"])
		}
	} else if kind == "none" {
		// The supervised backend's registration *is* its pidfile, so that is the thing that has to
		// be gone.
		art.Meta["uninstall_service_kind_supervised"] = "true"
	}

	// Retention, asked of the filesystem. An absent path is reported as unknown rather than as
	// retained: a check that could not look must not conclude the file survived.
	if logPath != "" {
		e, _ := g.Exec(ctx, sh.PathExists(logPath), opts)
		art.Meta["uninstall_log_retained"] = fmt.Sprintf("%v", e.ExitCode == 0)
	}

	// Beacon's own account of itself, recorded alongside the machine's. When the two disagree, that
	// disagreement is the finding.
	if s, err := g.Exec(ctx, "beacon endpoint status "+scope+" --json 2>&1", opts); err == nil {
		art.Meta["uninstall_status"] = oneLine(s.Stdout)
	}
}
