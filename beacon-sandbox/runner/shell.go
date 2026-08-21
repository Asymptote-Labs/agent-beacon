package runner

import (
	"encoding/base64"
	"fmt"
	"path"
	"strings"

	"github.com/asymptote-labs/agent-beacon/beacon-sandbox/sandbox"
)

// shell builds the guest-side scripts the runner needs, in the guest's own dialect.
//
// Every one of these was a bare POSIX string before Windows existed as a target. They are
// gathered behind an interface rather than sprinkled with `if windows` because the runner's
// value is that a Windows verdict is produced by *the same* control flow as a Linux one -- the
// canary, the sentinel gate, quiescence, collection and judging all stay single-path, and only
// the dialect differs. Branching inline is how the two would drift into one being more
// forgiving than the other.
type shell interface {
	// Quote renders a value as one literal argument.
	Quote(string) string
	// Join builds a guest path from segments.
	Join(...string) string
	// ProbeFile prints __FOUND__ then the contents, or __MISSING__. Never empty, so a failed
	// exec is distinguishable from an empty file.
	ProbeFile(path string) string
	// FileSize prints the byte count, or 0 when absent.
	FileSize(path string) string
	// ReadFile prints the whole file, or nothing when absent.
	ReadFile(path string) string
	// TailBytes prints the last n bytes.
	TailBytes(path string, n int) string
	// WithEnv prefixes a script with one environment assignment.
	WithEnv(name, value, script string) string
	// CIExecSession wraps an agent invocation in `beacon ci exec`, propagating its status.
	CIExecSession(logPath, workDir string, claudeArgs []string) string
	// PlainSession runs the agent with no collector wrapper, for install scenarios.
	PlainSession(workDir string, claudeArgs []string) string
	// ArgvSampler starts the background credential sampler.
	ArgvSampler() string
	// ArgvVerdictPath is where the sampler writes what it saw.
	ArgvVerdictPath() string
	// PathExists exits zero when the path is there, non-zero when it is not.
	PathExists(path string) string
	// ServiceQuery asks the platform's service manager about a service by name, or returns empty
	// when this backend has no manager to ask.
	ServiceQuery(kind, label string) string
	// ServiceAbsent interprets a ServiceQuery result as "no such service".
	//
	// Split from ServiceQuery because absence is spelled differently on each platform, and getting it
	// wrong is the dangerous direction: a query whose failure is misread as absence would report a
	// still-registered service as removed, which is precisely the bug an uninstall check exists to
	// catch.
	ServiceAbsent(kind string, exitCode int, output string) bool
}

// shellFor returns the dialect for a platform.
func shellFor(p sandbox.Platform) shell {
	if p.IsWindows() {
		return powerShell{}
	}
	return posixShell{}
}

// ---------------------------------------------------------------------------
// POSIX
// ---------------------------------------------------------------------------

type posixShell struct{}

func (posixShell) Quote(s string) string { return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'" }

func (posixShell) Join(parts ...string) string { return path.Join(parts...) }

// ProbeFile always emits a marker, so its output is never ambiguous. `cat f || echo
// __MISSING__` produced empty stdout for both a failed exec and a legitimately empty sentinel
// file, which would have misreported the second as a broken probe.
func (s posixShell) ProbeFile(p string) string {
	return fmt.Sprintf("if [ -f %[1]s ]; then echo __FOUND__; cat %[1]s; else echo __MISSING__; fi",
		s.Quote(p))
}

func (s posixShell) FileSize(p string) string {
	return "wc -c < " + s.Quote(p) + " 2>/dev/null || echo 0"
}

func (s posixShell) ReadFile(p string) string {
	return "cat " + s.Quote(p) + " 2>/dev/null || true"
}

func (s posixShell) TailBytes(p string, n int) string {
	return fmt.Sprintf("tail -c %d %s 2>/dev/null || true", n, s.Quote(p))
}

func (s posixShell) WithEnv(name, value, script string) string {
	return name + "=" + s.Quote(value) + " " + script
}

// CIExecSession wraps the agent in `beacon ci exec`.
//
// The child is wrapped in `sh -c` so the agent's stdout is redirected *inside* `beacon ci
// exec`. Redirecting outside it would interleave Beacon's own report with the agent's JSON in
// one file, which made the first M0 run's output unparseable.
//
// The script must exit with `beacon ci exec`'s status, not with `tail`'s. Ending on the tail
// made every session look successful no matter how ci exec fared, so a collector that failed
// outright was indistinguishable from one that worked.
func (s posixShell) CIExecSession(logPath, _ string, claudeArgs []string) string {
	inner := "claude " + strings.Join(claudeArgs, " ") + " > claude-out.json 2> claude-err.txt"
	// stderr is surfaced on failure, not just stdout. `beacon ci exec` reports why it could not
	// start or supervise the collector on stderr, and tailing only ci-out.txt meant a non-zero exit
	// arrived with no explanation at all -- the reason was sitting in a file nothing ever read.
	return fmt.Sprintf("beacon ci exec --harness claude --log-path %s -- sh -c %s "+
		"> ci-out.txt 2> ci-err.txt; rc=$?; echo CI_EXEC_RC=$rc; "+
		"tail -c 400 ci-out.txt; "+
		"if [ \"$rc\" -ne 0 ]; then echo '--- ci exec stderr ---'; tail -c 800 ci-err.txt; fi; "+
		"exit $rc",
		s.Quote(logPath), s.Quote(inner))
}

func (posixShell) PlainSession(_ string, claudeArgs []string) string {
	return "claude " + strings.Join(claudeArgs, " ") +
		" > claude-out.json 2> claude-err.txt; echo CLAUDE_RC=$?"
}

func (posixShell) ArgvVerdictPath() string { return "/tmp/beacon-sandbox-argv-verdict" }

// ArgvSampler starts a background sampler that watches for the injected credential in the
// guest process table for as long as the agent session runs.
//
// It has to run concurrently with the session, and an earlier version did not: the scan
// executed after `beacon ci exec` had already returned, so every process that might have held
// the key in argv was already gone and ARGV_CLEAN was close to vacuous -- a disclosure claim
// resting on an observation never made.
//
// Both `ps` output and every /proc/<pid>/cmdline are checked, since a shell can hide argv from
// one but not the other. It runs as root because another user's cmdline is otherwise
// unreadable, and a check that can only see its own process proves nothing.
//
// The matcher must not carry the key in its own argv or the scan finds itself: a first version
// used `grep -qF "$KEY"`, which the shell expands into grep's argv, so the `ps` in the same
// pipeline saw the grep process and reported a leak the tool had created. awk reads the key
// from ENVIRON instead, and the sampler's own processes are skipped as a second guard.
func (s posixShell) ArgvSampler() string {
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
  # process holding the key is not evidence of anything.
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
echo ARGV_SAMPLER_STARTED`, s.ArgvVerdictPath(), "/tmp/beacon-sandbox-argv-sampler.sh")
}

// ---------------------------------------------------------------------------
// PowerShell
// ---------------------------------------------------------------------------

type powerShell struct{}

// Quote renders a PowerShell single-quoted literal, where the only escape is a doubled quote.
// Single-quoted is deliberate: PowerShell performs no expansion inside it, so a value carrying
// `$`, backtick or `%` cannot become something else.
func (powerShell) Quote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", "''") + "'"
}

// Join uses backslashes, since these paths are consumed by Windows APIs and shown to a human
// reading a verdict. Forward slashes would mostly work and would look wrong in every report.
func (powerShell) Join(parts ...string) string {
	cleaned := make([]string, 0, len(parts))
	for _, p := range parts {
		if p = strings.Trim(strings.ReplaceAll(p, "/", `\`), `\`); p != "" {
			cleaned = append(cleaned, p)
		}
	}
	if len(cleaned) == 0 {
		return ""
	}
	// A leading drive letter keeps its separator. "C:" alone is a *relative* path on Windows --
	// the current directory on that drive -- so trimming the backslash off "C:\work" would
	// silently change which directory a script writes to. Everything else joins plainly, since
	// the trim above already removed interior duplicates.
	if len(cleaned) > 1 && strings.HasSuffix(cleaned[0], ":") {
		return cleaned[0] + `\` + strings.Join(cleaned[1:], `\`)
	}
	return strings.Join(cleaned, `\`)
}

// ProbeFile mirrors the POSIX contract: a marker on every branch, so empty stdout can only mean
// the exec itself failed.
//
// -LiteralPath throughout, not -Path: -Path treats [, ] and ? as wildcards, and a sentinel or
// work directory containing them would silently resolve to nothing.
func (s powerShell) ProbeFile(p string) string {
	return fmt.Sprintf("if (Test-Path -LiteralPath %[1]s -PathType Leaf) { '__FOUND__'; "+
		"Get-Content -LiteralPath %[1]s -Raw } else { '__MISSING__' }", s.Quote(p))
}

func (s powerShell) FileSize(p string) string {
	return fmt.Sprintf("if (Test-Path -LiteralPath %[1]s -PathType Leaf) "+
		"{ (Get-Item -LiteralPath %[1]s).Length } else { 0 }", s.Quote(p))
}

func (s powerShell) ReadFile(p string) string {
	return fmt.Sprintf("if (Test-Path -LiteralPath %[1]s -PathType Leaf) "+
		"{ Get-Content -LiteralPath %[1]s -Raw }", s.Quote(p))
}

// TailBytes reads bytes rather than lines, matching `tail -c`, and clamps the start so a file
// shorter than n is returned whole instead of throwing.
func (s powerShell) TailBytes(p string, n int) string {
	q := s.Quote(p)
	return fmt.Sprintf("if (Test-Path -LiteralPath %[1]s -PathType Leaf) { "+
		"$b = [System.IO.File]::ReadAllBytes(%[1]s); "+
		"$start = [Math]::Max(0, $b.Length - %[2]d); "+
		"[System.Text.Encoding]::UTF8.GetString($b, $start, $b.Length - $start) }", q, n)
}

// WithEnv sets a process-scoped variable. PowerShell has no `VAR=value cmd` prefix form -- the
// POSIX spelling is a syntax error here, which is why this is on the interface at all.
func (s powerShell) WithEnv(name, value, script string) string {
	return fmt.Sprintf("$env:%s = %s; %s", name, s.Quote(value), script)
}

// CIExecSession wraps the agent in `beacon ci exec`, keeping the agent's stdout redirected
// inside the wrapper so Beacon's own report cannot interleave with the agent's JSON.
//
// The inner command travels as base64 into a file rather than as a nested quoted string. The
// prompt carries a canary, arbitrary punctuation and possibly quotes, and it would otherwise
// have to survive two levels of PowerShell quoting -- the failure mode being a prompt that
// arrives subtly altered, which looks exactly like the agent ignoring instructions. This is the
// same reasoning the nested-container guest already uses for crossing two shells.
func (s powerShell) CIExecSession(logPath, workDir string, claudeArgs []string) string {
	inner := s.innerSessionScript(workDir, claudeArgs)
	return fmt.Sprintf(`%s
& beacon ci exec --harness claude --log-path %s -- pwsh -NoProfile -File $innerPath > ci-out.txt 2> ci-err.txt
$rc = $LASTEXITCODE
if ($null -eq $rc) { $rc = 0 }
"CI_EXEC_RC=$rc"
%s
if ($rc -ne 0) {
  '--- ci exec stderr ---'
%s
}
exit $rc`,
		inner, s.Quote(logPath), s.TailBytes("ci-out.txt", 400), s.TailBytes("ci-err.txt", 800))
}

// PlainSession runs the agent directly, for install scenarios where a persistent collector is
// already running as a service.
func (s powerShell) PlainSession(workDir string, claudeArgs []string) string {
	return fmt.Sprintf(`%s
& pwsh -NoProfile -File $innerPath
$rc = $LASTEXITCODE
if ($null -eq $rc) { $rc = 0 }
"CLAUDE_RC=$rc"`, s.innerSessionScript(workDir, claudeArgs))
}

// innerSessionScript materializes the agent invocation as a .ps1 and leaves its path in
// $innerPath.
//
// Set-Location inside the inner script is load-bearing: the redirects are relative, and the
// process that ends up running this file is a grandchild of the shell that wrote it, so its
// working directory cannot be assumed.
func (s powerShell) innerSessionScript(workDir string, claudeArgs []string) string {
	body := fmt.Sprintf("Set-Location -LiteralPath %s\n"+
		"& claude %s > claude-out.json 2> claude-err.txt\n"+
		"exit $LASTEXITCODE\n",
		s.Quote(workDir), strings.Join(claudeArgs, " "))
	enc := base64.StdEncoding.EncodeToString([]byte(body))
	return fmt.Sprintf(`$innerPath = Join-Path $env:TEMP 'beacon-sandbox-session.ps1'
[System.IO.File]::WriteAllText($innerPath, [System.Text.Encoding]::UTF8.GetString([System.Convert]::FromBase64String('%s')))`, enc)
}

func (powerShell) ArgvVerdictPath() string {
	return `C:\Windows\Temp\beacon-sandbox-argv-verdict`
}

// ArgvSampler is the Windows half of the credential-in-argv check, and it has the same job as
// the POSIX one: observe the process table *while* the agent runs, and never carry the key in
// its own command line.
//
// Win32_Process replaces both /proc and ps -- Windows has no equivalent of either, and
// CommandLine on that class is the only place a full argv is readable for another process.
// The key is compared with String.Contains against a value read from the environment, so it
// never reaches an argument list; the sampler also skips its own PID, the same second guard the
// POSIX version keeps.
//
// Start-Process rather than a background job: a PowerShell job dies with the shell that created
// it, and this shell exits as soon as Exec returns -- which is before the session even starts.
// The sampler has to outlive its parent, exactly as nohup makes it on POSIX.
func (s powerShell) ArgvSampler() string {
	verdict := s.ArgvVerdictPath()
	scriptPath := `C:\Windows\Temp\beacon-sandbox-argv-sampler.ps1`
	body := `param([string]$Verdict)
$key = $env:ANTHROPIC_API_KEY
if (-not $key) {
  Set-Content -LiteralPath $Verdict -Value 'ARGV_CHECK_INVALID_NO_KEY samples=0'
  exit 0
}
$me = $PID
$n = 0
$sawAgent = 0
# Bounded so a leaked runner cannot spin forever; the job ends long before this.
while ($n -lt 900) {
  $n++
  $procs = @(Get-CimInstance Win32_Process -ErrorAction SilentlyContinue)
  foreach ($p in $procs) {
    if ($p.ProcessId -eq $me) { continue }
    $cl = $p.CommandLine
    if (-not $cl) { continue }
    if ($cl -like '*claude*') { $sawAgent = 1 }
    if ($cl.Contains($key)) {
      Set-Content -LiteralPath $Verdict -Value "ARGV_LEAK samples=$n saw_agent=$sawAgent via=win32process"
      exit 0
    }
  }
  Set-Content -LiteralPath $Verdict -Value "ARGV_CLEAN samples=$n saw_agent=$sawAgent"
  Start-Sleep -Seconds 1
}
`
	enc := base64.StdEncoding.EncodeToString([]byte(body))
	return fmt.Sprintf(`$ErrorActionPreference = 'Continue'
Remove-Item -LiteralPath %[1]s -ErrorAction SilentlyContinue
if (-not $env:ANTHROPIC_API_KEY) {
  Set-Content -LiteralPath %[1]s -Value 'ARGV_CHECK_INVALID_NO_KEY samples=0'
  exit 0
}
[System.IO.File]::WriteAllText(%[2]s, [System.Text.Encoding]::UTF8.GetString([System.Convert]::FromBase64String('%[3]s')))
Start-Process -FilePath 'pwsh' -ArgumentList '-NoProfile','-NonInteractive','-File',%[2]s,%[1]s -WindowStyle Hidden
'ARGV_SAMPLER_STARTED'`, s.Quote(verdict), s.Quote(scriptPath), enc)
}

// PathExists uses test, which is a shell builtin and needs no external binary -- a minimal image may
// have no coreutils.
func (posixShell) PathExists(path string) string {
	return "test -e " + (posixShell{}).Quote(path)
}

// ServiceQuery asks the manager that actually registered the service.
//
// Only systemd and launchd have anything to ask. The supervised backend's registration is its
// pidfile, which the caller checks as a path instead, so this returns empty rather than inventing a
// query that would always succeed.
func (posixShell) ServiceQuery(kind, label string) string {
	switch kind {
	case "systemd":
		// list-unit-files rather than status: status reports on a unit that no longer exists with the
		// same exit code as one that is merely stopped, so it cannot distinguish removed from
		// inactive. This prints nothing at all once the unit file is gone.
		return "systemctl list-unit-files " + (posixShell{}).Quote(label) + " --no-legend 2>&1"
	case "launchd":
		return "launchctl print system/" + (posixShell{}).Quote(label) + " 2>&1"
	}
	return ""
}

// ServiceAbsent reads the query output for each manager's spelling of "not there".
func (posixShell) ServiceAbsent(kind string, exitCode int, output string) bool {
	trimmed := strings.TrimSpace(output)
	switch kind {
	case "systemd":
		// A removed unit produces no rows. Non-zero with output is a different problem -- systemd
		// unreachable, say -- and must not read as absence.
		return trimmed == "" || strings.Contains(trimmed, "0 unit files listed")
	case "launchd":
		// launchctl prints this for a job it does not know about.
		return exitCode != 0 && (strings.Contains(trimmed, "Could not find service") ||
			strings.Contains(trimmed, "No such process"))
	}
	return false
}

// PathExists uses Test-Path and turns it into an exit code, because the caller compares exit codes
// across platforms rather than parsing output.
func (powerShell) PathExists(path string) string {
	return "if (Test-Path -LiteralPath " + (powerShell{}).Quote(path) + ") { exit 0 } else { exit 1 }"
}

// ServiceQuery asks the Service Control Manager. The supervised backend has no manager, same as on
// POSIX, so it gets no query.
func (powerShell) ServiceQuery(kind, label string) string {
	if kind == "windows-service" {
		return "sc.exe query " + (powerShell{}).Quote(label) + " 2>&1"
	}
	return ""
}

// ServiceAbsent looks for error 1060, which is what sc.exe returns for a service that does not
// exist. Matched on the number rather than the message text, because the message is localized and a
// German or Japanese machine would otherwise report a removed service as still present.
func (powerShell) ServiceAbsent(kind string, exitCode int, output string) bool {
	if kind != "windows-service" {
		return false
	}
	return strings.Contains(output, "1060")
}
