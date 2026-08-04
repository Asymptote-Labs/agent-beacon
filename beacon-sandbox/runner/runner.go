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
		},
	}

	// Fingerprint the developer's own Beacon state before anything runs, so a stray local
	// install would be caught rather than assumed impossible.
	//
	// The comparison and the meta write happen together in this defer, and deliberately so.
	// An earlier version wrote meta.json inline before returning, which meant host_state --
	// set here, after that write -- never reached disk. The live verdict was still correct
	// because judging happens after Run returns, but an offline `verify` read the field as
	// empty and treated the host check as clean. An unrunnable check that reads as a pass is
	// the exact failure this tool exists to prevent. Writing here also means an early error
	// return still leaves the metadata behind for debugging.
	hostBefore := hostguard.Take()
	defer func() {
		art.HostDiff = hostguard.Compare(hostBefore, hostguard.Take())
		art.Meta["host_state"] = art.HostDiff.Describe()
		if b, err := json.MarshalIndent(art.Meta, "", "  "); err == nil {
			_ = os.WriteFile(filepath.Join(runDir, "meta.json"), append(b, '\n'), 0o600)
		}
	}()

	logf("building image (cached layers are free)")
	imgSpec, err := image.Build(image.Spec{
		RepoRoot:      opts.RepoRoot,
		ClaudeVersion: opts.ClaudeVersion,
	}, logf)
	if err != nil {
		return art, err
	}
	img, err := p.EnsureImage(ctx, imgSpec)
	if err != nil {
		return art, fmt.Errorf("ensure image: %w", err)
	}
	art.Meta["image"] = img.Ref()
	logf("image %s", img.Ref())

	inst, err := p.Launch(ctx, img, sandbox.LaunchSpec{
		CPU:         2,
		MemLimitMiB: 4096,
		Timeout:     sc.Timeout() + 5*time.Minute, // headroom for setup and collection
		Workdir:     image.WorkDir,
		Secrets:     inlineSecrets(opts.Creds),
		SecretNames: providerSecrets(opts.Creds),
		BlockEgress: sc.BlockEgress,
		EgressAllowDomains: []string{
			// Only what the agent needs. Anything Beacon itself tried to reach would fail
			// here, which is how the local-only posture gets asserted rather than assumed.
			"api.anthropic.com", "statsig.anthropic.com", "*.anthropic.com",
		},
	})
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
		User: image.AgentUser, Dir: image.WorkDir, HomeDir: image.AgentHome,
		PreserveEnv: true, PathPrepend: image.PathPrepend(), Timeout: 5 * time.Minute,
	}

	// Finish the artifact layer: the snapshot captured the files, but chmod and symlinks
	// have to happen in a live instance.
	if _, err := p.Exec(ctx, inst, image.PostPushLayers(), sandbox.ExecOpts{User: "root", Timeout: time.Minute}); err != nil {
		return art, fmt.Errorf("prepare binaries: %w", err)
	}

	if v, err := p.Exec(ctx, inst, "beacon version; claude --version; id -un; uname -m", agent); err == nil {
		art.Meta["versions"] = oneLine(v.Stdout)
		logf("versions: %s", art.Meta["versions"])
	}

	sentinel := scenario.Expand(sc.Sentinel, canary, "", image.WorkDir)
	if sc.Setup != "" {
		setup := scenario.Expand(sc.Setup, canary, sentinel, image.WorkDir)
		if r, err := p.Exec(ctx, inst, setup, agent); err != nil || r.ExitCode != 0 {
			return art, fmt.Errorf("scenario setup failed (rc=%d): %v %s", r.ExitCode, err, oneLine(r.Stderr))
		}
	}

	logPath := filepath.ToSlash(filepath.Join(image.WorkDir, "runtime.jsonl"))
	prompt := scenario.Expand(sc.Prompt, canary, sentinel, image.WorkDir)

	// Start the credential sampler BEFORE the session, so it observes the agent's own
	// processes while they are alive. See argvSamplerScript.
	if _, err := p.Exec(ctx, inst, argvSamplerScript(), sandbox.ExecOpts{
		User: "root", Timeout: time.Minute,
	}); err != nil {
		logf("argv sampler failed to start: %v", err)
	}

	// Two ways to get a collector, and which one a scenario uses is the whole point of the
	// distinction. `beacon ci exec` wraps the session in an ephemeral collector -- a real
	// shipping path, used in GitHub Actions and cloud agent sandboxes, needing no service
	// manager. A persistent install instead exercises `beacon endpoint install`: service
	// manager selection, unit installation, status reporting, and whether an installed
	// collector actually receives telemetry. Nothing else in the suite can tell you that.
	var res sandbox.Result
	if sc.Install != nil {
		installedLog, err := doInstall(ctx, p, inst, sc, agent, &art, logf)
		if err != nil {
			return art, err
		}
		logPath = installedLog
		logf("running session against the installed endpoint (budget $%.2f, timeout %s)",
			sc.Budget(), sc.Timeout())
		res, err = p.Exec(ctx, inst, plainSessionScript(sc, prompt), sandbox.ExecOpts{
			User: image.AgentUser, Dir: workDirFor(sc), HomeDir: image.AgentHome,
			PreserveEnv: true, PathPrepend: image.PathPrepend(), Timeout: sc.Timeout() + time.Minute,
		})
		if err != nil {
			logf("session exec error: %v", err)
		}
		art.Meta["session"] = oneLine(res.Stdout)
	} else {
		logf("running session (budget $%.2f, timeout %s)", sc.Budget(), sc.Timeout())
		var err error
		res, err = p.Exec(ctx, inst, sessionScript(sc, prompt, logPath), sandbox.ExecOpts{
			User: image.AgentUser, Dir: workDirFor(sc), HomeDir: image.AgentHome,
			PreserveEnv: true, PathPrepend: image.PathPrepend(), Timeout: sc.Timeout() + time.Minute,
		})
		if err != nil {
			logf("session exec error: %v", err)
		}
		art.Meta["ci_exec"] = oneLine(res.Stdout)
		// Recorded explicitly so a failing collector is visible in the run metadata rather
		// than inferred from a missing log later.
		art.Meta["ci_exec_rc"] = fmt.Sprintf("%d", res.ExitCode)
		if res.ExitCode != 0 {
			logf("beacon ci exec exited %d -- the collected log may be incomplete", res.ExitCode)
		}
	}

	// Claude's own result JSON tells us whether the session itself succeeded, which
	// separates an agent/auth failure from a capture failure.
	//
	// Read by absolute path. The session runs in the scenario's cwd, so a relative read from
	// the default working directory silently found nothing whenever `cwd:` was set -- s05 was
	// the only such scenario and it reported session_ok=false on an otherwise perfect run,
	// disabling exactly the signal that distinguishes a dead agent from a capture gap.
	outPath := shq(sessionFile(sc, "claude-out.json"))
	errPath := shq(sessionFile(sc, "claude-err.txt"))
	if r, err := p.Exec(ctx, inst, "cat "+outPath+" 2>/dev/null || true", agent); err == nil {
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
			if e, err2 := p.Exec(ctx, inst, "tail -c 800 "+errPath+" 2>/dev/null || true", agent); err2 == nil {
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
		probe := fmt.Sprintf("if [ -f %[1]s ]; then echo __FOUND__; cat %[1]s; else echo __MISSING__; fi",
			shq(sentinel))
		r, execErr := p.Exec(ctx, inst, probe, agent)
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
	if r, err := p.Exec(ctx, inst, "cat "+shq(argvVerdictPath)+" 2>/dev/null || true",
		sandbox.ExecOpts{User: "root", Timeout: time.Minute}); err == nil {
		switch {
		case strings.Contains(r.Stdout, "ARGV_LEAK"):
			art.SecretInArgv, art.ArgvCheckRan = true, true
			art.Meta["argv_leak_detail"] = oneLine(r.Stdout)
			logf("argv leak detail: %s", art.Meta["argv_leak_detail"])
		case strings.Contains(r.Stdout, "ARGV_CLEAN"):
			art.SecretInArgv, art.ArgvCheckRan = false, true
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
	stable, waited := waitForQuiescence(ctx, p, inst, agent, logPath, logf)
	art.Meta["quiescence_seconds"] = fmt.Sprintf("%.0f", waited.Seconds())
	art.Meta["quiescent"] = fmt.Sprintf("%v", stable)

	local := filepath.Join(runDir, "runtime.jsonl")
	if err := p.Get(ctx, inst, logPath, local); err != nil {
		return art, fmt.Errorf("collect runtime log: %w", err)
	}
	art.RuntimeLog = local
	logf("collected %s", local)

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

// sessionFile resolves a file the agent session wrote, relative to the directory that session
// actually ran in.
func sessionFile(sc scenario.Scenario, name string) string {
	return filepath.ToSlash(filepath.Join(workDirFor(sc), name))
}

// claudeFlags builds the agent invocation shared by both session shapes.
func claudeFlags(sc scenario.Scenario, prompt string) []string {
	flags := []string{
		"-p", shq(prompt),
		"--output-format", "json",
		"--dangerously-skip-permissions",
		fmt.Sprintf("--max-budget-usd %.2f", sc.Budget()),
	}
	if len(sc.Tools) > 0 {
		flags = append(flags, "--tools", shq(strings.Join(sc.Tools, ",")))
	} else if sc.Tools != nil {
		flags = append(flags, "--tools", shq(""))
	}
	if len(sc.DisallowedTools) > 0 {
		flags = append(flags, "--disallowedTools", shq(strings.Join(sc.DisallowedTools, ",")))
	}
	return flags
}

// sessionScript builds the in-guest command.
//
// The child is wrapped in `sh -c` so Claude's stdout is redirected *inside* `beacon ci exec`.
// Redirecting outside it would interleave Beacon's own report with Claude's JSON in one file,
// which made the first M0 run's output unparseable.
func sessionScript(sc scenario.Scenario, prompt, logPath string) string {
	inner := "claude " + strings.Join(claudeFlags(sc, prompt), " ") +
		" > claude-out.json 2> claude-err.txt"
	// The script must exit with `beacon ci exec`'s status, not with `tail`'s. Ending on the tail
	// made every session look successful to p.Exec no matter how ci exec fared, and nothing read
	// CI_EXEC_RC either -- so a collector that failed outright was indistinguishable from one
	// that worked. Reported by the Copilot reviewer.
	return fmt.Sprintf("beacon ci exec --harness claude --log-path %s -- sh -c %s "+
		"> ci-out.txt 2> ci-err.txt; rc=$?; echo CI_EXEC_RC=$rc; "+
		"tail -c 400 ci-out.txt; exit $rc",
		shq(logPath), shq(inner))
}

// waitForQuiescence polls the log until its size stops changing, bounded.
func waitForQuiescence(ctx context.Context, p sandbox.Provider, inst sandbox.Instance,
	opts sandbox.ExecOpts, logPath string, logf func(string, ...any)) (bool, time.Duration) {

	const (
		interval  = 3 * time.Second
		stableFor = 9 * time.Second
		maxWait   = 90 * time.Second
	)
	start := time.Now()
	var last string
	stableSince := time.Time{}

	for time.Since(start) < maxWait {
		r, err := p.Exec(ctx, inst, "wc -c < "+shq(logPath)+" 2>/dev/null || echo 0", opts)
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

func workDirFor(sc scenario.Scenario) string {
	if sc.Cwd == "" {
		return image.WorkDir
	}
	return filepath.ToSlash(filepath.Join(image.WorkDir, sc.Cwd))
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
# Bounded so a leaked sandbox cannot spin forever; the instance is destroyed long before this.
while [ "$n" -lt 900 ]; do
  n=$((n+1))
  if ps -eo args= 2>/dev/null | scan; then
    echo "ARGV_LEAK samples=$n via=ps" > "$VERDICT"
    exit 0
  fi
  for f in /proc/[0-9]*/cmdline; do
    [ -r "$f" ] || continue
    if tr '\0' '\n' < "$f" 2>/dev/null | scan; then
      echo "ARGV_LEAK samples=$n via=proc" > "$VERDICT"
      exit 0
    fi
  done
  echo "ARGV_CLEAN samples=$n" > "$VERDICT"
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
func doInstall(ctx context.Context, p sandbox.Provider, inst sandbox.Instance,
	sc scenario.Scenario, agent sandbox.ExecOpts, art *Artifacts, logf func(string, ...any)) (string, error) {

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
	// System-mode install requires root; user mode must not have it, or the install would
	// write system paths while claiming to be a user install.
	opts := agent
	if system {
		opts = sandbox.ExecOpts{User: "root", Dir: image.WorkDir, Timeout: 5 * time.Minute,
			PathPrepend: image.PathPrepend()}
	}
	art.Meta["install_mode"] = in.Mode
	art.Meta["install_service"] = in.Service

	// Dry run first. It is free, and it is where a plan that advertises the wrong service
	// manager for the host would show up — so a regression there needs no second scenario.
	if r, err := p.Exec(ctx, inst, "beacon endpoint install "+scope+svc+" --harness claude --dry-run 2>&1", opts); err == nil {
		art.Meta["install_dry_run"] = oneLine(r.Stdout)
		logf("dry-run plan: %s", art.Meta["install_dry_run"])
	}

	r, err := p.Exec(ctx, inst, "beacon endpoint install "+scope+svc+" --harness claude 2>&1", opts)
	art.Meta["install_output"] = oneLine(r.Stdout + r.Stderr)
	if err != nil || r.ExitCode != 0 {
		logf("install FAILED rc=%d: %s", r.ExitCode, art.Meta["install_output"])
		return "", fmt.Errorf("endpoint install failed (rc=%d): %s",
			r.ExitCode, oneLine(r.Stdout+r.Stderr))
	}
	logf("install ok: %s", art.Meta["install_output"])

	logPath, err := installedLogPath(ctx, p, inst, scope, opts, in, art, logf)
	if err != nil {
		return "", err
	}
	// Doctor output is recorded whether or not it passes: on a failure it is the first thing
	// worth reading, and it costs one exec.
	if d, err := p.Exec(ctx, inst, "beacon endpoint doctor "+scope+" --json 2>&1", opts); err == nil {
		art.Meta["install_doctor"] = oneLine(d.Stdout)
	}
	return logPath, nil
}

// installedLogPath reads `endpoint status --json` for the service state and log path.
//
// A status that cannot be parsed is not fatal — the run can still proceed against the
// documented default path — but ExpectStatusRunning is enforced, since a scenario asking for a
// running service and silently accepting an unparseable status would verify nothing.
func installedLogPath(ctx context.Context, p sandbox.Provider, inst sandbox.Instance,
	scope string, opts sandbox.ExecOpts, in *scenario.Install, art *Artifacts,
	logf func(string, ...any)) (string, error) {

	s, err := p.Exec(ctx, inst, "beacon endpoint status "+scope+" --json 2>&1", opts)
	if err == nil {
		art.Meta["install_status"] = oneLine(s.Stdout)
		var status struct {
			LogPath string `json:"log_path"`
			Service struct {
				Label   string `json:"label"`
				Loaded  bool   `json:"loaded"`
				Running bool   `json:"running"`
				Kind    string `json:"kind"`
				Message string `json:"message"`
			} `json:"service"`
		}
		if json.Unmarshal([]byte(s.Stdout), &status) == nil {
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
			if status.LogPath != "" {
				return status.LogPath, nil
			}
		}
	}
	if in.ExpectStatusRunning {
		return "", fmt.Errorf("could not read service status, so a running service cannot be " +
			"confirmed; refusing to report a pass the run did not earn")
	}
	logf("status unparseable; falling back to the documented default log path")
	if in.Mode == "system" {
		return "/var/log/beacon-agent/runtime.jsonl", nil
	}
	return image.AgentHome + "/.beacon/endpoint/logs/runtime.jsonl", nil
}

// plainSessionScript runs the agent with no collector wrapper, for install scenarios where a
// persistent collector is already running as a service.
func plainSessionScript(sc scenario.Scenario, prompt string) string {
	return "claude " + strings.Join(claudeFlags(sc, prompt), " ") +
		" > claude-out.json 2> claude-err.txt; echo CLAUDE_RC=$?"
}
