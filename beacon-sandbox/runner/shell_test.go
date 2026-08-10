package runner

import (
	"encoding/base64"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/asymptote-labs/agent-beacon/beacon-sandbox/sandbox"
	"github.com/asymptote-labs/agent-beacon/beacon-sandbox/scenario"
)

// The dialect must follow the *provider's* platform, not the host's. A Mac dispatching a Windows
// run is the normal case, so keying off runtime.GOOS would generate bash for a Windows guest and
// every script would fail as a syntax error.
func TestShellFollowsTheGuestPlatformNotTheHost(t *testing.T) {
	if _, ok := shellFor(sandbox.PlatformWindows).(powerShell); !ok {
		t.Errorf("a Windows guest must get the PowerShell dialect, got %T",
			shellFor(sandbox.PlatformWindows))
	}
	if _, ok := shellFor(sandbox.PlatformPosix).(posixShell); !ok {
		t.Errorf("a POSIX guest must get the POSIX dialect, got %T",
			shellFor(sandbox.PlatformPosix))
	}
}

// Quoting is the seam where a canary or prompt gets silently altered, and an altered prompt looks
// exactly like the agent ignoring instructions. PowerShell's only escape inside a single-quoted
// literal is a doubled quote, and nothing else is expanded -- which is why single quotes are used.
func TestPowerShellQuotingIsLiteral(t *testing.T) {
	sh := powerShell{}
	cases := map[string]string{
		`plain`:            `'plain'`,
		`it's`:             `'it''s'`,
		`$env:PATH`:        `'$env:PATH'`,
		"back`tick":        "'back`tick'",
		`100% done`:        `'100% done'`,
		`C:\Program Files`: `'C:\Program Files'`,
	}
	for in, want := range cases {
		if got := sh.Quote(in); got != want {
			t.Errorf("Quote(%q) = %s, want %s", in, got, want)
		}
	}
}

// Every dialect must be able to emit both markers. Empty stdout has to mean "the exec failed" and
// nothing else -- `cat f || echo __MISSING__` produced empty output for both a failed exec and a
// legitimately empty sentinel, and those are an infrastructure problem and a real absence of agent
// work respectively.
func TestBothDialectsProbeUnambiguously(t *testing.T) {
	for _, sh := range []shell{posixShell{}, powerShell{}} {
		probe := sh.ProbeFile("/tmp/out.txt")
		for _, want := range []string{"__FOUND__", "__MISSING__"} {
			if !strings.Contains(probe, want) {
				t.Errorf("%T probe cannot emit %s: %s", sh, want, probe)
			}
		}
	}
}

// -LiteralPath, not -Path. -Path treats [, ] and ? as wildcards, so a sentinel or work directory
// containing one would resolve to nothing and the probe would report the agent idle.
func TestPowerShellFileOperationsDoNotGlob(t *testing.T) {
	sh := powerShell{}
	scripts := map[string]string{
		"ProbeFile": sh.ProbeFile(`C:\work\a[1].txt`),
		"FileSize":  sh.FileSize(`C:\work\a[1].txt`),
		"ReadFile":  sh.ReadFile(`C:\work\a[1].txt`),
		"TailBytes": sh.TailBytes(`C:\work\a[1].txt`, 400),
	}
	// Leading whitespace is what distinguishes the *parameter* -Path from the cmdlet name
	// Test-Path, and -LiteralPath does not contain "-Path" at all.
	bareParam := regexp.MustCompile(`\s-Path\b`)
	for name, script := range scripts {
		if bareParam.MatchString(script) {
			t.Errorf("%s uses -Path, which treats [ ] ? as wildcards: %s", name, script)
		}
	}
}

// PowerShell has no `VAR=value cmd` prefix form; the POSIX spelling is a syntax error there. This
// is the whole reason WithEnv is on the interface, and it is the same shape as the bug that makes
// every hook command Beacon writes unusable on Windows.
func TestWithEnvUsesEachDialectsOwnForm(t *testing.T) {
	if got := (posixShell{}).WithEnv("SUDO_USER", "agent", "beacon x"); got != "SUDO_USER='agent' beacon x" {
		t.Errorf("posix WithEnv = %q", got)
	}
	got := powerShell{}.WithEnv("SUDO_USER", "runneradmin", "beacon x")
	if !strings.HasPrefix(got, "$env:SUDO_USER = 'runneradmin';") {
		t.Errorf("powershell WithEnv must assign $env:, got %q", got)
	}
	if strings.HasPrefix(got, "SUDO_USER=") {
		t.Error("the POSIX inline-assignment form is a syntax error in PowerShell")
	}
}

// The Windows sampler has the same contract as the POSIX one: it must outlive the shell that
// started it, loop rather than sample once, and record whether the agent was ever in view.
func TestWindowsArgvSamplerDetachesAndLoops(t *testing.T) {
	got := powerShell{}.ArgvSampler()

	// A PowerShell background job dies with its parent, and the parent exits as soon as Exec
	// returns -- before the session even starts. Start-Process survives it, as nohup does on POSIX.
	if !strings.Contains(got, "Start-Process") {
		t.Error("the sampler must detach; a background job dies with the shell that created it")
	}
	if strings.Contains(got, "Start-Job") {
		t.Error("Start-Job does not outlive its parent shell, so the sampler would never observe the session")
	}
	if !strings.HasSuffix(strings.TrimSpace(got), "'ARGV_SAMPLER_STARTED'") {
		t.Error("the starter must announce itself, or a sampler that never ran looks like one that found nothing")
	}

	body := decodedSamplerBody(t, got)
	if !strings.Contains(body, "while ($n -lt") || !strings.Contains(body, "Start-Sleep") {
		t.Error("a single sample cannot observe a live session; it must loop")
	}
	// Win32_Process.CommandLine is the only place another process's full argv is readable on
	// Windows -- there is no /proc and no ps.
	if !strings.Contains(body, "Win32_Process") || !strings.Contains(body, ".CommandLine") {
		t.Errorf("the sampler must read command lines from Win32_Process:\n%s", body)
	}
	for _, want := range []string{"saw_agent=$sawAgent", "ARGV_CLEAN", "ARGV_LEAK"} {
		if !strings.Contains(body, want) {
			t.Errorf("sampler body is missing %q:\n%s", want, body)
		}
	}
}

// The matcher must never carry the key in its own command line, or the scan finds itself and
// reports a leak the tool created. On POSIX that meant awk reading ENVIRON; here it means
// String.Contains against a value read from $env, plus skipping its own PID.
func TestWindowsArgvSamplerNeverPutsTheKeyInACommandLine(t *testing.T) {
	got := powerShell{}.ArgvSampler()
	body := decodedSamplerBody(t, got)

	if !strings.Contains(body, "$key = $env:ANTHROPIC_API_KEY") {
		t.Error("the key must be read from the environment inside the sampler")
	}
	if !strings.Contains(body, "$cl.Contains($key)") {
		t.Errorf("the comparison must be a method call so the key never reaches an argument list:\n%s", body)
	}
	// Select-String and findstr both take the pattern as an argument, which is the self-poisoning
	// shape: the sampler's own process table entry would then contain the key.
	for _, bad := range []string{"Select-String", "findstr"} {
		if strings.Contains(body, bad) {
			t.Errorf("%s takes the pattern as an argument, which puts the key in argv", bad)
		}
	}
	if !strings.Contains(body, "$p.ProcessId -eq $me") {
		t.Error("the sampler must skip its own process, the second guard against finding itself")
	}
}

// With no key there is nothing to search for, so the sampler must say so rather than write a clean
// verdict. A check that could not run must never read as one that passed.
func TestWindowsArgvSamplerReportsInvalidRatherThanCleanWithoutAKey(t *testing.T) {
	got := powerShell{}.ArgvSampler()

	if !strings.Contains(got, "ARGV_CHECK_INVALID_NO_KEY") {
		t.Error("a missing key must produce an explicit invalid marker")
	}
	// The guard has to come first in the starter, before anything that could emit a clean verdict.
	guard := strings.Index(got, "if (-not $env:ANTHROPIC_API_KEY)")
	start := strings.Index(got, "Start-Process")
	if guard < 0 || start < 0 || guard > start {
		t.Error("the no-key guard must precede launching the sampler")
	}
	body := decodedSamplerBody(t, got)
	bodyGuard := strings.Index(body, "if (-not $key)")
	bodyClean := strings.Index(body, "ARGV_CLEAN")
	if bodyGuard < 0 || bodyClean < 0 || bodyGuard > bodyClean {
		t.Error("the sampler body must check for the key before any path that can emit ARGV_CLEAN")
	}
}

// The session's inner command travels as base64 rather than as a nested quoted string. The prompt
// carries a canary and arbitrary punctuation, and surviving two levels of PowerShell quoting is
// how a prompt arrives subtly altered -- which is indistinguishable from the agent ignoring it.
func TestWindowsSessionScriptCarriesThePromptOutOfBandAndPropagatesStatus(t *testing.T) {
	sh := powerShell{}
	prompt := `write 'BEACON_SANDBOX_abc' to $out -- 100% now`
	got := sh.CIExecSession(`C:\work\runtime.jsonl`, `C:\work`,
		claudeFlags(stubScenario(), prompt, sh))

	if strings.Contains(got, prompt) {
		t.Error("the prompt must not appear inline; it should be base64 in the staged inner script")
	}
	inner := decodeFirstBase64(t, got)
	// The prompt must survive quoting unchanged, which is the property that matters: an altered
	// prompt is indistinguishable from the agent ignoring instructions. Checked by undoing
	// PowerShell's only single-quote escape and requiring the original back.
	if !strings.Contains(inner, sh.Quote(prompt)) {
		t.Errorf("the staged inner script must carry the quoted prompt:\n%s", inner)
	}
	if roundTripped := strings.ReplaceAll(sh.Quote(prompt), "''", "'"); roundTripped != "'"+prompt+"'" {
		t.Errorf("quoting does not round-trip: %q became %q", prompt, roundTripped)
	}
	// Set-Location is load-bearing: the redirects are relative and the process that runs this file
	// is a grandchild of the shell that wrote it, so its working directory cannot be assumed.
	if !strings.Contains(inner, "Set-Location -LiteralPath 'C:\\work'") {
		t.Errorf("the inner script must set its own working directory:\n%s", inner)
	}
	if !strings.Contains(inner, "> claude-out.json 2> claude-err.txt") {
		t.Error("the agent's stdout must be redirected inside the collector wrapper, not outside it")
	}
	// Same invariant as the POSIX side: the script exits with ci exec's status, and the diagnostic
	// tail runs before that exit so its output is still captured.
	if !strings.Contains(got, "$rc = $LASTEXITCODE") || !strings.Contains(got, "exit $rc") {
		t.Errorf("the script must capture and re-raise ci exec's status:\n%s", got)
	}
	if strings.Index(got, "exit $rc") < strings.Index(got, "ReadAllBytes('ci-out.txt')") {
		t.Error("the tail should run before the exit so its output is still captured")
	}
}

// $LASTEXITCODE is $null when no native command has run yet, and `exit $null` exits 0 -- which
// would report a session that never launched as a success.
func TestWindowsSessionScriptsGuardAgainstANullExitCode(t *testing.T) {
	sh := powerShell{}
	for name, script := range map[string]string{
		"CIExecSession": sh.CIExecSession("log", `C:\work`, []string{"-p", "'hi'"}),
		"PlainSession":  sh.PlainSession(`C:\work`, []string{"-p", "'hi'"}),
	} {
		if !strings.Contains(script, "if ($null -eq $rc) { $rc = 0 }") {
			t.Errorf("%s must normalize a null $LASTEXITCODE:\n%s", name, script)
		}
	}
}

// The generated PowerShell is assembled in Go, so a syntax error in it is invisible to the
// compiler and would surface as "the check did not run" -- which a verdict reports as unverified
// rather than failing loudly. Parsed with PowerShell's own parser when one is available.
func TestGeneratedPowerShellParses(t *testing.T) {
	pwsh, err := exec.LookPath("pwsh")
	if err != nil {
		t.Skip("no pwsh on PATH; the windows-sandbox CI job covers this")
	}
	sh := powerShell{}
	scripts := map[string]string{
		"ArgvSampler":   sh.ArgvSampler(),
		"SamplerBody":   decodedSamplerBody(t, sh.ArgvSampler()),
		"CIExecSession": sh.CIExecSession(`C:\work\runtime.jsonl`, `C:\work`, claudeFlags(stubScenario(), "hi", sh)),
		"PlainSession":  sh.PlainSession(`C:\work`, claudeFlags(stubScenario(), "hi", sh)),
		"ProbeFile":     sh.ProbeFile(`C:\work\out.txt`),
		"FileSize":      sh.FileSize(`C:\work\out.txt`),
		"ReadFile":      sh.ReadFile(`C:\work\out.txt`),
		"TailBytes":     sh.TailBytes(`C:\work\out.txt`, 400),
		"VersionProbe":  versionProbe(sandbox.PlatformWindows),
	}
	for name, script := range scripts {
		path := filepath.Join(t.TempDir(), name+".ps1")
		if err := os.WriteFile(path, []byte(script), 0o600); err != nil {
			t.Fatal(err)
		}
		// Parse without executing: the parser reports syntax errors and nothing runs.
		check := `$errors = $null
[void][System.Management.Automation.Language.Parser]::ParseFile('` + path + `', [ref]$null, [ref]$errors)
if ($errors) { $errors | ForEach-Object { $_.Message }; exit 1 }`
		if out, err := exec.Command(pwsh, "-NoProfile", "-NonInteractive", "-Command", check).CombinedOutput(); err != nil {
			t.Errorf("generated %s is not valid PowerShell: %v\n%s\n--- script ---\n%s",
				name, err, out, script)
		}
	}
}

// stubScenario is a minimal scenario for script-shape tests.
func stubScenario() scenario.Scenario {
	return scenario.Scenario{ID: "w00-probe", Prompt: "unused; the prompt is passed separately"}
}

// decodedSamplerBody extracts and decodes the staged sampler script.
func decodedSamplerBody(t *testing.T, starter string) string {
	t.Helper()
	return decodeLastBase64(t, starter)
}

var base64Literal = regexp.MustCompile(`FromBase64String\('([A-Za-z0-9+/=]+)'\)`)

func decodeFirstBase64(t *testing.T, script string) string {
	t.Helper()
	m := base64Literal.FindStringSubmatch(script)
	if m == nil {
		t.Fatalf("no base64 payload found in:\n%s", script)
	}
	return decode(t, m[1])
}

func decodeLastBase64(t *testing.T, script string) string {
	t.Helper()
	all := base64Literal.FindAllStringSubmatch(script, -1)
	if len(all) == 0 {
		t.Fatalf("no base64 payload found in:\n%s", script)
	}
	return decode(t, all[len(all)-1][1])
}

func decode(t *testing.T, enc string) string {
	t.Helper()
	b, err := base64.StdEncoding.DecodeString(enc)
	if err != nil {
		t.Fatalf("payload is not valid base64: %v", err)
	}
	return string(b)
}
