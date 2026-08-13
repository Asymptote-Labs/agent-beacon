// beacon-sandbox drives Beacon verification in a disposable Linux sandbox.
//
//	beacon-sandbox run                         run scenarios, collect, and judge
//	beacon-sandbox verify runs/<dir>           re-judge already-collected artifacts (free, offline)
//	beacon-sandbox clean                       remove local run artifacts
//
// `verify` is the one to reach for while iterating: it needs no sandbox, no model, and no
// network, so changing what counts as correct costs nothing.
package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/asymptote-labs/agent-beacon/beacon-sandbox/check"
	"github.com/asymptote-labs/agent-beacon/beacon-sandbox/credentials"
	"github.com/asymptote-labs/agent-beacon/beacon-sandbox/dispatch"
	"github.com/asymptote-labs/agent-beacon/beacon-sandbox/runner"
	"github.com/asymptote-labs/agent-beacon/beacon-sandbox/sandbox"
	"github.com/asymptote-labs/agent-beacon/beacon-sandbox/scenario"
)

const appName = "beacon-sandbox"

// randomHex makes the planted self-test credential unique per invocation, so it cannot collide
// with anything already in a collected log.
func randomHex() string {
	b := make([]byte, 8)
	if _, err := rand.Read(b); err != nil {
		return "fallback0000000"
	}
	return hex.EncodeToString(b)
}

func main() {
	if len(os.Args) < 2 {
		usage()
		os.Exit(2)
	}
	var err error
	switch os.Args[1] {
	case "doctor":
		err = cmdDoctor(os.Args[2:])
	case "run":
		err = cmdRun(os.Args[2:])
	case "verify":
		err = cmdVerify(os.Args[2:])
	case "diff":
		err = cmdDiff(os.Args[2:])
	case "clean":
		err = cmdClean(os.Args[2:])
	case "-h", "--help", "help":
		usage()
		return
	default:
		usage()
		os.Exit(2)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

func usage() {
	fmt.Fprint(os.Stderr, `beacon-sandbox -- verify Beacon against real agent activity in a disposable sandbox

  doctor  [--fix] [--json]        check prerequisites and say how to fix what is missing
  run     [--scenario ID] [--repeat N] [--keep-sandbox] [--suite DIR] [--provider NAME]
  verify  <run-dir>...   [--mutate MODE]
  diff    <before-dir> <after-dir>
  clean   [--dir runs]

Where scenarios run (--provider):
  modal    a disposable Linux sandbox. The default, and Linux-only
  github   dispatch to a GitHub Actions Windows runner, then collect and judge locally
  local    this machine, for use inside a disposable CI runner

Anthropic credential, in the order they resolve:
  --modal-secret NAME    a secret already stored with the provider; the value never enters
                         this process, so the artifact leak check reports unverified
  --api-key-command CMD  a command that prints the key (op read, vault kv get, ...)
  ANTHROPIC_API_KEY      environment variable

New here? Run doctor -- it checks every prerequisite and prints the exact fix:
  go run ./cmd/beacon-sandbox doctor --fix
`)
}

func repoRoot() (string, error) {
	// The beacon-sandbox module sits one level under the repo root.
	wd, err := os.Getwd()
	if err != nil {
		return "", err
	}
	// Terminate on the fixed point of filepath.Dir rather than on a hardcoded "/" or ".".
	// On Windows, Dir of a drive root returns that same root, so the hardcoded form spun
	// forever whenever the marker was missing instead of reporting a clear error.
	for dir := wd; ; {
		if _, err := os.Stat(filepath.Join(dir, "cli", "beacon", "go.mod")); err == nil {
			return dir, nil
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
		dir = parent
	}
	return "", fmt.Errorf("could not locate the repo root from %s", wd)
}

func cmdRun(args []string) error {
	fs := flag.NewFlagSet("run", flag.ExitOnError)
	suite := fs.String("suite", "", "scenario directory: absolute, or relative to the repo root (default beacon-sandbox/scenarios)")
	only := fs.String("scenario", "", "run only this scenario id")
	repeat := fs.Int("repeat", 1, "run each scenario N times; a class captured in some runs but not all is flakiness, not absence")
	keep := fs.Bool("keep-sandbox", false, "leave the sandbox running for debugging")
	outDir := fs.String("out", "", "artifact directory (default beacon-sandbox/runs)")
	claudeVer := fs.String("claude-version", "", "pin Claude Code version (default: image default)")
	modalSecret := fs.String("modal-secret", "", "name of a Modal secret holding ANTHROPIC_API_KEY; the value never enters this process")
	keyCommand := fs.String("api-key-command", "", "command whose stdout is the Anthropic API key (e.g. 'op read op://vault/anthropic/key')")
	provider := fs.String("provider", "modal", "where scenarios run: modal (disposable Linux), github (dispatch to a Windows runner), or local (this machine)")
	allowHostMutation := fs.Bool("allow-host-mutation", false, "with --provider local, run on a machine this tool cannot prove is disposable")
	repo := fs.String("repo", "", "with --provider github, the owner/name to dispatch to (default: inferred from this checkout)")
	ref := fs.String("ref", "", "with --provider github, the branch or tag to run (default: the workflow's default branch)")
	fs.Parse(args)

	root, err := repoRoot()
	if err != nil {
		return err
	}
	// A dispatched run authenticates the agent from the workflow's environment secret, so no local
	// credential is needed or read. Resolving one anyway would fail on a machine that has none, and
	// printing it would claim this process supplied a key it never sent.
	var creds credentials.Resolved
	if *provider == "github" {
		fmt.Printf("credential: the %s workflow's environment secret (nothing is read locally)\n",
			dispatch.Workflow)
	} else {
		creds, err = credentials.Resolve(credentials.Options{
			ProviderSecretName: *modalSecret,
			KeyCommand:         *keyCommand,
		})
		if err != nil {
			return err
		}
		fmt.Printf("credential: %s\n", creds.Describe())
	}

	// A plain directory path, absolute or relative to the repo root. An earlier version
	// rewrote any value without a path separator to the default directory, so `--suite core`
	// and every other name silently selected the default and no alternative suite was
	// reachable.
	scDir := strings.TrimSpace(*suite)
	switch {
	case scDir == "":
		scDir = filepath.Join(root, "beacon-sandbox", "scenarios")
	case !filepath.IsAbs(scDir):
		scDir = filepath.Join(root, scDir)
	}
	sui, err := scenario.LoadSuite(scDir)
	if err != nil {
		return err
	}
	out := *outDir
	if out == "" {
		out = filepath.Join(root, "beacon-sandbox", "runs")
	}

	// A dispatched run is a different shape: the harness executes inside a CI job, and this side
	// only starts it, waits, collects and judges. Handled before a Provider is opened because
	// there is no machine here to drive.
	if *provider == "github" {
		return dispatchRun(sui, dispatchOptions{
			repo: *repo, ref: *ref, scenario: *only, claudeVersion: *claudeVer,
			outDir: out, suiteDir: scDir,
		})
	}

	ctx := context.Background()
	prov, closeProv, err := openProvider(ctx, *provider, *allowHostMutation)
	if err != nil {
		return err
	}
	defer closeProv()

	// Scenarios are not portable, so run only the ones written for this guest -- and say how many
	// were set aside. `run` with no --scenario is the "everything" command, and silently returning
	// a subset of it would read as full coverage.
	target := platformOf(prov)
	sui, skipped := sui.For(target)
	if skipped > 0 {
		fmt.Printf("suite: %d %s scenario(s); %d written for another platform and not run\n",
			len(sui.Scenarios), target, skipped)
	}
	if len(sui.Scenarios) == 0 {
		return fmt.Errorf("no %s scenarios in %s, so this run would assert nothing", target, scDir)
	}

	var verdicts []check.Verdict
	matched := false
	for _, sc := range sui.Scenarios {
		if *only != "" && sc.ID != *only {
			continue
		}
		matched = true
		for i := 0; i < *repeat; i++ {
			label := sc.ID
			if *repeat > 1 {
				label = fmt.Sprintf("%s (run %d/%d)", sc.ID, i+1, *repeat)
			}
			fmt.Printf("\n=== %s ===\n", label)
			art, err := runner.Run(ctx, prov, sc, runner.Options{
				RepoRoot: root, OutDir: out, Creds: creds,
				ClaudeVersion: *claudeVer, KeepInstance: *keep,
				Log: func(f string, a ...any) { fmt.Printf("  "+f+"\n", a...) },
			})
			if err != nil {
				fmt.Printf("  run failed: %v\n", err)
				// A failed run still collected host and argv state, and those signals matter
				// most precisely when something went wrong. Fold them in rather than replacing
				// the whole verdict with a generic message.
				v := check.Verdict{Scenario: sc.ID, Meta: art.Meta,
					Reason: "run did not complete: " + err.Error()}
				safetyOf(&v, art)
				v.Outcome = check.Fail
				fmt.Print(indent(v.Report()))
				verdicts = append(verdicts, v)
				continue
			}
			v, log := judge(sc, art, creds, "")
			fmt.Print(indent(v.Report()))
			if art.Dir != "" {
				_ = v.WriteJSON(filepath.Join(art.Dir, "verdict.json"))
				_ = v.Fingerprint(log).WriteJSON(filepath.Join(art.Dir, "fingerprint.json"))
			}
			verdicts = append(verdicts, v)
		}
	}
	// A --scenario that matched nothing must not exit 0 with an empty summary: the caller asked
	// for a specific check and got none, which is a failure to run rather than a clean result.
	// A typo'd id, or one belonging to the other platform, both land here.
	if *only != "" && !matched {
		return fmt.Errorf("no %s scenario with id %q in %s; `--provider %s` runs %s scenarios",
			target, *only, scDir, *provider, target)
	}
	return summarize(verdicts)
}

// openProvider resolves the --provider choice, returning a cleanup function so a Modal client is
// always closed.
func openProvider(ctx context.Context, name string, allowHostMutation bool) (sandbox.Provider, func(), error) {
	switch name {
	case "modal":
		p, err := sandbox.NewModal(ctx, appName)
		if err != nil {
			return nil, func() {}, err
		}
		return p, p.Close, nil
	case "local":
		p, err := sandbox.NewLocalExec(sandbox.LocalExecOptions{AllowHostMutation: allowHostMutation})
		if err != nil {
			return nil, func() {}, err
		}
		return p, func() {}, nil
	default:
		return nil, func() {}, fmt.Errorf("unknown --provider %q; want modal, github, or local", name)
	}
}

type dispatchOptions struct {
	repo, ref, scenario, claudeVersion, outDir, suiteDir string
}

// dispatchRun starts the Windows workflow, waits for it, and judges what it collected.
//
// No credential is resolved here, and that is not an omission: the agent's key lives in the
// workflow's environment secret and never reaches this process. The consequence is recorded
// rather than hidden -- the artifact leak check has no value to search for, so it reports
// unverified, exactly as the --modal-secret path does. The in-runner argv sampler still runs,
// because there the key genuinely is present.
func dispatchRun(sui scenario.Suite, opts dispatchOptions) error {
	// Fail before spending a runner on a scenario that does not exist or is not a Windows one.
	// The workflow would otherwise start, install an agent, and only then find nothing to run.
	windows, _ := sui.For(scenario.PlatformWindows)
	if len(windows.Scenarios) == 0 {
		return fmt.Errorf("no windows scenarios in %s, so a dispatched run would assert nothing",
			opts.suiteDir)
	}
	if opts.scenario != "" {
		found := false
		for _, sc := range windows.Scenarios {
			if sc.ID == opts.scenario {
				found = true
				break
			}
		}
		if !found {
			return fmt.Errorf("no windows scenario with id %q in %s; --provider github runs "+
				"windows scenarios", opts.scenario, opts.suiteDir)
		}
	}

	res, dispatchErr := dispatch.Run(dispatch.Options{
		Repo: opts.repo, Ref: opts.ref, Scenario: opts.scenario,
		ClaudeVersion: opts.claudeVersion, OutDir: opts.outDir,
		Log: func(f string, a ...any) { fmt.Printf("  "+f+"\n", a...) },
	})
	// Judge whatever came back before reporting the dispatch error. A failing job still uploads
	// its artifacts, and those artifacts carry the host-state and argv findings that matter most
	// precisely when something went wrong -- the same reason cmdRun folds them in on a failed run
	// rather than replacing the verdict with a generic message.
	root, err := repoRoot()
	if err != nil {
		return err
	}
	var verdicts []check.Verdict
	for _, dir := range res.RunDirs {
		v, err := judgeRunDir(root, dir, "")
		if err != nil {
			fmt.Printf("  could not judge %s: %v\n", dir, err)
			continue
		}
		fmt.Print(indent(v.Report()))
		verdicts = append(verdicts, v)
	}
	if dispatchErr != nil {
		if len(verdicts) > 0 {
			_ = summarize(verdicts)
		}
		return dispatchErr
	}
	// GitHub's own conclusion is reported alongside the verdicts rather than instead of them: a
	// red job with a passing scenario means the workflow broke around the run, and a green job
	// with a failing scenario is impossible by construction but worth noticing if it ever happens.
	if res.Conclusion != "success" {
		fmt.Printf("  the workflow run concluded %q: %s\n", res.Conclusion, res.URL)
	}
	return summarize(verdicts)
}

// platformOf maps a provider's guest platform onto the scenario vocabulary.
func platformOf(p sandbox.Provider) scenario.Platform {
	if p.Platform().IsWindows() {
		return scenario.PlatformWindows
	}
	return scenario.PlatformLinux
}

// judge is the pure step: artifacts in, verdict out. Returns the parsed log too so callers
// can build a fingerprint without re-reading it.
func judge(sc scenario.Scenario, art runner.Artifacts, creds credentials.Resolved, planted string) (check.Verdict, check.Log) {
	v := check.Verdict{Scenario: sc.ID, Meta: art.Meta}

	// Host escape and credential-in-argv come from run metadata, not from the log, so they must
	// be reported even when the log is unreadable. An earlier version returned before this on a
	// ReadLog error, which meant a collection failure could bury a real disclosure or a
	// clobbered local ~/.beacon behind a generic "could not read the log". A collection problem
	// is the weakest reason imaginable to stop reporting the strongest findings.
	// Reported by Cursor Bugbot.
	safetyOf(&v, art)

	log, err := check.ReadLog(art.RuntimeLog)
	if err != nil {
		v.Outcome = check.Fail
		v.Reason = "could not read the collected runtime log: " + err.Error()
		return v, log
	}

	// Only real secrets go here. The canary is meant to appear in the log, not to be
	// absent from it.
	secrets := check.Secrets{}
	if creds.LeakCheckPossible() {
		secrets.Values = map[string]string{credentials.EnvVar: creds.Value}
	} else {
		secrets.Withheld = map[string]string{credentials.EnvVar: creds.WithheldReason()}
	}
	// A self-test plants a synthetic credential, and the leak check has to be searching for it or
	// the mutation proves nothing. Searched in addition to the real credential, never instead of
	// it, so a mutated run still reports on the genuine one.
	if planted != "" {
		if secrets.Values == nil {
			secrets.Values = map[string]string{}
		}
		secrets.Values["planted self-test credential"] = planted
	}
	check.Invariants(&v, log, secrets)
	check.Expectations(&v, log, sc, art.Canary, check.Sentinel{
		Declared: sc.Sentinel != "",
		Present:  art.SentinelPresent,
		Probed:   art.SentinelProbed,
		Detail:   art.SentinelDetail,
	}, check.Session{Known: art.SessionKnown, OK: art.SessionOK})
	// Read from meta rather than from a live probe, so `verify` re-judges a saved run's removal for
	// free exactly as it re-judges its capture.
	check.Uninstall(&v, check.Removal{
		Ran:          art.Meta["uninstall_ran"],
		ExitCode:     art.Meta["uninstall_rc"],
		Output:       art.Meta["uninstall_output"],
		ServiceKind:  art.Meta["service_kind"],
		ServiceLabel: art.Meta["service_label"],
		ServiceGone:  art.Meta["uninstall_service_gone"],
		ServiceQuery: art.Meta["uninstall_service_query"],
		LogRetained:  art.Meta["uninstall_log_retained"],
		Status:       art.Meta["uninstall_status"],
	})
	lifecycle := check.Lifecycle{
		Restarted:           art.Meta["reinstall_restarted"],
		PIDBefore:           art.Meta["reinstall_pid_before"],
		PIDAfter:            art.Meta["reinstall_pid_after"],
		ReinstallErr:        art.Meta["reinstall_error"],
		UnprivilegedRefused: art.Meta["unprivileged_uninstall_refused"],
		UnprivilegedOutput:  art.Meta["unprivileged_uninstall_output"],

		RollbackInstallRC:      art.Meta["rollback_install_rc"],
		RollbackCollectorCount: art.Meta["rollback_collector_count"],
		RollbackStatus:         art.Meta["rollback_status"],
	}
	check.Reinstall(&v, lifecycle)
	check.UnprivilegedUninstall(&v, lifecycle)
	check.FailedReinstallRollback(&v, lifecycle)
	v.Resolve()
	return v, log
}

// safetyOf folds the out-of-band safety signals into a verdict.
//
// Shared by the normal and the run-failed paths so neither can quietly omit them: host escape and
// credential disclosure are the two findings that must survive every other kind of failure.
func safetyOf(v *check.Verdict, art runner.Artifacts) {
	check.Safety(v, check.HostSafety{
		HostChanged:   art.Meta["host_state"],
		SecretInArgv:  art.Meta["secret_in_argv"] == "true",
		ArgvCheckRan:  art.Meta["argv_check_ran"] == "true",
		Disposability: art.Meta["disposability"],
	})
}

// decodeSentinel reads the sentinel signal from run metadata without inventing information.
//
// Same shape as decodeSession, and for the same reason. sentinel_present predates sentinel_probed,
// and the older probe (`cat f || echo __MISSING__`) returned empty stdout on a failed guest exec --
// which was recorded as present=false. So a recorded true is unambiguous and is trusted, while a
// recorded false without an accompanying sentinel_probed means either the agent was idle or the
// probe broke, and must decode as unprobed rather than as a confident "the agent did nothing".
//
// This is the sibling of the bug Cursor Bugbot reported for sessions. Fixing only the session half
// would have left the identical conflation one field away.
func decodeSentinel(meta map[string]string) (probed, present bool) {
	switch {
	case meta["sentinel_probed"] != "":
		return meta["sentinel_probed"] == "true", meta["sentinel_present"] == "true"
	case meta["sentinel_present"] == "true":
		return true, true
	default:
		return false, false
	}
}

// decodeSession reads the session signal from run metadata without inventing information.
//
// Absent metadata must not decode as a *known* failure. Defaulting Known to true while OK defaulted
// to false meant an artifact lacking both keys read as "the session definitely failed", which
// short-circuited sentinel-less scenarios to INCONCLUSIVE without evaluating a single expectation.
// Reported by Cursor Bugbot.
//
// session_ok predates session_known, and the old writer only ever set it true when claude-out.json
// parsed, so a recorded true is unambiguous. A recorded false is not -- it meant either a failed
// session or an unreadable result, the very conflation session_known exists to end -- so an older
// artifact is trusted only in the true case.
func decodeSession(meta map[string]string) (known, ok bool) {
	switch {
	case meta["session_known"] != "":
		return meta["session_known"] == "true", meta["session_ok"] == "true"
	case meta["session_ok"] == "true":
		return true, true
	default:
		return false, false
	}
}

// offlineCredential reconstructs, for an offline re-judge, what the leak check can search for.
//
// It must honor what the original run used, and it must be able to tell when it cannot. Two
// distinct false-clean holes had to be closed here, both reported by Cursor Bugbot:
//
//   - a provider-secret run has no local value at all, so reaching for the ambient
//     ANTHROPIC_API_KEY searched the artifacts for an unrelated key
//   - even for env and command runs, the key present at verify time may differ from the one used
//     during capture (rotated, different account, different shell), and searching for the wrong
//     value finds nothing while a real disclosure of the original goes unreported
//
// The recorded fingerprint closes the second: a mismatch means this process cannot search for the
// right value, so the check reports unverified instead of clean. Silence is only allowed to mean
// "clean" when the value being searched for is provably the one the run used.
func offlineCredential(meta map[string]string) credentials.Resolved {
	if credentials.Source(meta["credential_source"]) == credentials.SourceProviderSecret {
		return credentials.Resolved{
			Source:             credentials.SourceProviderSecret,
			ProviderSecretName: meta["credential_secret_name"],
		}
	}
	key := strings.TrimSpace(os.Getenv(credentials.EnvVar))
	if key == "" {
		return credentials.Resolved{Source: credentials.SourceNone}
	}
	// A run recorded before fingerprints existed cannot be checked either way; treat the
	// unknown as unverified rather than assuming the keys match.
	want := meta["credential_fingerprint"]
	if want == "" || want != credentials.Fingerprint(key) {
		return credentials.Resolved{Source: credentials.SourceMismatch}
	}
	return credentials.Resolved{Source: credentials.SourceEnv, Value: key}
}

func cmdVerify(args []string) error {
	fs := flag.NewFlagSet("verify", flag.ExitOnError)
	mutate := fs.String("mutate", "", "corrupt the input to self-test the checks: "+
		"drop-commands|drop-action:<action>|corrupt-line|plant-secret")
	fs.Parse(args)
	dirs := fs.Args()
	if len(dirs) == 0 {
		return fmt.Errorf("usage: beacon-sandbox verify <run-dir>...")
	}

	root, err := repoRoot()
	if err != nil {
		return err
	}
	var verdicts []check.Verdict
	for _, dir := range dirs {
		v, err := judgeRunDir(root, dir, *mutate)
		if err != nil {
			return err
		}
		fmt.Print(v.Report())
		verdicts = append(verdicts, v)
	}
	return summarize(verdicts)
}

// judgeRunDir judges one collected run directory, offline.
//
// Shared by `verify` and by the dispatched-run path rather than duplicated, because the decoding
// it performs carries several corrections that are easy to lose in a second copy: the sentinel and
// session signals must decode as *unknown* rather than as known failures when the metadata
// predates them, and the credential the leak check searches for must be provably the one the run
// used or the check reports unverified. A second implementation would drift back into false greens.
func judgeRunDir(root, dir, mutate string) (check.Verdict, error) {
	meta := map[string]string{}
	if b, err := os.ReadFile(filepath.Join(dir, "meta.json")); err == nil {
		_ = json.Unmarshal(b, &meta)
	}
	scID := meta["scenario"]
	if scID == "" {
		scID = filepath.Base(dir)
	}
	sc, err := findScenario(root, scID)
	if err != nil {
		return check.Verdict{}, err
	}

	logPath := filepath.Join(dir, "runtime.jsonl")
	var planted string
	if mutate != "" {
		// The uninstall checks read what the runner observed about the machine, not the event log, so
		// damaging the log could never exercise them. Their mutations rewrite the recorded observation
		// instead -- which is the same idea applied to a different artifact, and the only way a check
		// that reads meta can be shown to be capable of failing.
		if applyMetaMutation(meta, mutate) {
			fmt.Printf("(mutation %q applied to run metadata: a PASS becoming FAIL is the check working)\n", mutate)
		} else {
			logPath, planted, err = applyMutation(logPath, mutate)
			if err != nil {
				return check.Verdict{}, err
			}
			defer os.Remove(logPath)
			fmt.Printf("(mutation %q applied: a PASS becoming FAIL is the check working)\n", mutate)
		}
	}

	art := runner.Artifacts{RuntimeLog: logPath, Meta: meta}
	art.Canary = meta["canary"]
	// Sentinel state is not re-derivable offline, so trust what the run recorded -- including
	// the detail, which is the evidence a finding quotes. Dropping it made re-judged verdicts
	// show an empty detail for a sentinel the capture path had already described. Reported by
	// Cursor Bugbot.
	art.SentinelDetail = meta["sentinel_detail"]
	art.SentinelProbed, art.SentinelPresent = decodeSentinel(meta)
	art.SessionKnown, art.SessionOK = decodeSession(meta)
	creds := offlineCredential(meta)
	v, log := judge(sc, art, creds, planted)
	// Refresh both artifacts, or neither. Only the fingerprint was rewritten before, so a
	// re-judge with changed expectations left verdict.json holding the old outcome while
	// fingerprint.json held the new one -- the two files on disk actively disagreeing about
	// the same run, with anything reading the verdict trusting a stale judgment. Reported by
	// Cursor Bugbot, and visible in this repo's own run directories.
	//
	// Neither is written for a mutated run: a self-test deliberately damages the input, so
	// persisting its verdict would overwrite the run's real record with a fabricated failure.
	if mutate == "" {
		if err := v.WriteJSON(filepath.Join(dir, "verdict.json")); err != nil {
			fmt.Fprintf(os.Stderr, "warning: could not update verdict.json in %s: %v\n", dir, err)
		}
		if err := v.Fingerprint(log).WriteJSON(filepath.Join(dir, "fingerprint.json")); err != nil {
			fmt.Fprintf(os.Stderr, "warning: could not update fingerprint.json in %s: %v\n", dir, err)
		}
	}
	return v, nil
}

// cmdDiff compares two runs' capability fingerprints.
func cmdDiff(args []string) error {
	fs := flag.NewFlagSet("diff", flag.ExitOnError)
	fs.Parse(args)
	if fs.NArg() != 2 {
		return fmt.Errorf("usage: beacon-sandbox diff <before-dir> <after-dir>")
	}
	before, err := check.LoadFingerprint(filepath.Join(fs.Arg(0), "fingerprint.json"))
	if err != nil {
		return fmt.Errorf("read before fingerprint: %w", err)
	}
	after, err := check.LoadFingerprint(filepath.Join(fs.Arg(1), "fingerprint.json"))
	if err != nil {
		return fmt.Errorf("read after fingerprint: %w", err)
	}
	if before.Scenario != after.Scenario {
		return fmt.Errorf("refusing to compare different scenarios (%q vs %q)",
			before.Scenario, after.Scenario)
	}
	d := check.CompareFingerprints(before, after)
	fmt.Print(d.Describe())
	if d.HasRegression() {
		return fmt.Errorf("%d capability regression(s)", len(d.Regressions))
	}
	return nil
}

func findScenario(root, id string) (scenario.Scenario, error) {
	sui, err := scenario.LoadSuite(filepath.Join(root, "beacon-sandbox", "scenarios"))
	if err != nil {
		return scenario.Scenario{}, err
	}
	for _, s := range sui.Scenarios {
		if s.ID == id {
			return s, nil
		}
	}
	return scenario.Scenario{}, fmt.Errorf("no scenario with id %q", id)
}

// applyMutation deliberately damages a copy of the log. If the checks still pass afterwards
// they are not actually checking anything, so this is a self-test of the oracle.
func applyMutation(path, mode string) (mutated string, planted string, err error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return "", "", err
	}
	// strings.Split("", "\n") yields one empty element, so an empty log would otherwise be
	// treated as having a phantom line -- corrupt-line would "damage" a line that never existed
	// and report a working self-test against a log containing nothing.
	trimmed := strings.TrimRight(string(b), "\n")
	var lines []string
	if trimmed != "" {
		lines = strings.Split(trimmed, "\n")
	}
	var out []string

	// drop-action:<action> removes every event with that action. `drop-commands` is the
	// common case, and it is parameterized so the self-test can target whichever action a
	// given log actually contains -- a mutation that removes nothing proves nothing, which is
	// why dropping zero events is treated as an error below.
	if action, ok := strings.CutPrefix(mode, "drop-action:"); ok || mode == "drop-commands" {
		if mode == "drop-commands" {
			action = "command.executed"
		}
		dropped := 0
		for _, l := range lines {
			if actionOf(l) == action {
				dropped++
				continue
			}
			out = append(out, l)
		}
		if dropped == 0 {
			return "", "", fmt.Errorf("mutation %q removed nothing: no %q events in this log, "+
				"so the self-test would prove nothing", mode, action)
		}
		tmp := path + ".mutated"
		if err := os.WriteFile(tmp, []byte(strings.Join(out, "\n")+"\n"), 0o644); err != nil {
			return "", "", err
		}
		return tmp, planted, nil
	}

	// A mutation that changes nothing proves nothing, so every branch below has to actually
	// damage the log -- and the post-condition after the switch enforces that rather than
	// trusting each branch to have managed it. The drop-* path above already refused a no-op;
	// corrupt-line and plant-secret did not, so an empty log or a line without the expected
	// anchor produced an unchanged file, a passing check, and a self-test that had verified
	// nothing. Reported by Cursor Bugbot.
	switch mode {
	case "corrupt-line":
		out = append(out, lines...)
		if len(out) > 0 {
			out[len(out)/2] = `{"vendor":"beacon","this is not json`
		}
	case "plant-secret":
		// A synthetic credential, not the run's own.
		//
		// Planting the ambient ANTHROPIC_API_KEY only worked when it happened to be the key the
		// run was captured with: the leak check is withheld for a provider secret, a rotated
		// key, or a run predating credential fingerprints, so the planted value would never be
		// searched for and the self-test reported PASS having exercised nothing. The previous
		// fix refused in those cases, which was honest but meant the mutation was unavailable on
		// most existing run directories.
		//
		// Planting a value the judge is then told to search for removes the precondition
		// entirely: the leak check is exercised on any run, with any credential arrangement,
		// including none at all. The synthetic value is unique per invocation so it cannot
		// collide with real log content.
		planted = "sk-ant-beacon-sandbox-selftest-" + randomHex()
		out = append(out, lines...)
		if len(out) > 0 {
			out[0] = plantSecret(out[0], planted)
		}
		if len(out) == 0 || !strings.Contains(out[0], planted) {
			return "", "", fmt.Errorf("plant-secret could not insert a credential into %s: "+
				"the first line is empty or not an object, so the leak check would never see "+
				"it and the self-test would prove nothing", path)
		}
	default:
		return "", "", fmt.Errorf("unknown mutation %q (want drop-commands, drop-action:<action>, "+
			"corrupt-line, plant-secret, leave-service, or drop-retained-log)", mode)
	}

	// The shared post-condition: the log must differ from what it was. Compared on the trimmed
	// bodies, because appending the trailing newline made an empty log look like it had changed
	// from "" to "\n" -- a difference in the file that is no difference in the events, which is
	// the only thing a self-test cares about.
	body := strings.Join(out, "\n") + "\n"
	if len(out) == 0 || strings.TrimRight(body, "\n") == trimmed {
		return "", "", fmt.Errorf("mutation %q left %s unchanged, so the self-test would prove "+
			"nothing (is the log empty?)", mode, path)
	}
	tmp := path + ".mutated"
	if err := os.WriteFile(tmp, []byte(body), 0o644); err != nil {
		return "", "", err
	}
	return tmp, planted, nil
}

// plantSecret inserts a credential into a JSONL event line.
//
// The original anchored on `"message":`, which every real Beacon event happens to carry -- but a
// line without it was silently left untouched, and the caller then reported a passing self-test.
// Inserting before the closing brace works for any object, and the `"message":` form is kept first
// only because it keeps the result valid JSON in the common case, so the planted secret trips the
// leak check rather than the parse invariant.
func plantSecret(line, key string) string {
	if replaced := strings.Replace(line, `"message":`,
		fmt.Sprintf(`"leaked":%q,"message":`, key), 1); replaced != line {
		return replaced
	}
	if i := strings.LastIndex(line, "}"); i > 0 {
		return line[:i] + fmt.Sprintf(`,"leaked":%q}`, key) + line[i+1:]
	}
	return line
}

func cmdClean(args []string) error {
	fs := flag.NewFlagSet("clean", flag.ExitOnError)
	dir := fs.String("dir", "", "artifact directory (default beacon-sandbox/runs)")
	fs.Parse(args)
	root, err := repoRoot()
	if err != nil {
		return err
	}
	target := *dir
	if target == "" {
		target = filepath.Join(root, "beacon-sandbox", "runs")
	}
	fmt.Println("removing", target)
	return os.RemoveAll(target)
}

func summarize(verdicts []check.Verdict) error {
	if len(verdicts) == 0 {
		return fmt.Errorf("no scenarios ran")
	}
	var pass, fail, incon int
	fmt.Println("\n=== summary ===")
	for _, v := range verdicts {
		fmt.Printf("%-13s %s\n", v.Outcome, v.Scenario)
		switch v.Outcome {
		case check.Pass:
			pass++
		case check.Fail:
			fail++
		case check.Inconclusive:
			incon++
		}
	}
	fmt.Printf("%d passed, %d failed, %d inconclusive\n", pass, fail, incon)
	if incon > 0 {
		fmt.Println("inconclusive means the agent did not do the requested work -- retry those, " +
			"they are not capture failures")
	}
	if fail > 0 {
		return fmt.Errorf("%d scenario(s) failed", fail)
	}
	// Nothing verified is not the same as verified. Scripts and agents branch on exit status, so
	// exiting 0 here would let an unverified run read as a successful verification -- the same
	// vacuous pass this tool exists to prevent. Distinct wording from a failure, because the
	// remedy is a retry rather than an investigation.
	if pass == 0 && incon > 0 {
		return fmt.Errorf("nothing was verified: %d scenario(s) inconclusive, 0 passed; "+
			"retry rather than treating this as a successful run", incon)
	}
	return nil
}

// actionOf pulls event.action out of a raw JSONL line without a full decode, tolerating both
// compact and spaced JSON.
func actionOf(line string) string {
	var e struct {
		Event struct {
			Action string `json:"action"`
		} `json:"event"`
	}
	if json.Unmarshal([]byte(line), &e) != nil {
		return ""
	}
	return e.Event.Action
}

func indent(s string) string {
	var b strings.Builder
	for _, line := range strings.Split(strings.TrimRight(s, "\n"), "\n") {
		b.WriteString("  " + line + "\n")
	}
	return b.String()
}

// applyMetaMutation damages a recorded observation rather than the event log, and reports whether it
// recognised the mode.
//
// The uninstall checks judge what the runner saw the machine do — whether a service was still
// registered, whether configuration survived. None of that is in runtime.jsonl, so the log mutations
// cannot exercise them, and a check that cannot be made to fail is worse than no check at all.
//
// Each mode inverts exactly one observation, so the resulting failure names the check under test
// rather than a cascade.
func applyMetaMutation(meta map[string]string, mode string) bool {
	if meta == nil || meta["uninstall_ran"] != "true" {
		// Nothing to damage. A run that never uninstalled cannot demonstrate an uninstall check, and
		// silently "succeeding" here would report a self-test as passed when it never ran.
		return false
	}
	switch mode {
	case "leave-service":
		// The failure this simulates is the one that shipped: uninstall reports success while the
		// service stays registered and starts at boot.
		meta["uninstall_service_gone"] = "false"
		return true
	case "drop-retained-log":
		// The opposite direction: a removal that took the collected telemetry with it.
		meta["uninstall_log_retained"] = "false"
		return true
	}
	return false
}
