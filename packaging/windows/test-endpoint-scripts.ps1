# Validates the Windows packaging scripts without installing anything.
#
# The counterpart of packaging/macos/test-endpoint-scripts.sh, and it exists for the same reason: a
# packaging script only runs during an install, so a syntax error in one is invisible until someone
# installs the package -- at which point it fails inside an MSI custom action, where the diagnostic a
# user sees is a rollback and an error code.
#
# Parsing is most of the value here. PowerShell parses a whole file before executing any of it, so an
# unbalanced brace anywhere means nothing runs at all.
[CmdletBinding()]
param()

$ErrorActionPreference = 'Stop'
$scriptDir = Split-Path -Parent $MyInvocation.MyCommand.Path
$failures = 0

function Fail {
    param([string]$Message)
    Write-Error $Message
    $script:failures++
}

$scripts = @('install-endpoint.ps1', 'uninstall-endpoint.ps1', 'package-common.ps1', 'build-msi.ps1', 'smoke-restricted-log.ps1')
foreach ($name in $scripts) {
    $path = Join-Path $scriptDir $name
    if (-not (Test-Path -LiteralPath $path)) {
        Fail "missing script: $name"
        continue
    }
    $tokens = $null
    $errors = $null
    [System.Management.Automation.Language.Parser]::ParseFile($path, [ref]$tokens, [ref]$errors) | Out-Null
    if ($errors -and $errors.Count -gt 0) {
        foreach ($parseError in $errors) { Write-Output "  $($parseError.Extent.StartLineNumber): $($parseError.Message)" }
        Fail "$name does not parse"
    } else {
        Write-Output "ok: $name parses"
    }
}

# The install and uninstall contracts, asserted as text rather than by running them: both need
# elevation and a real machine, and what matters here is that the flags have not drifted from what
# the other platforms' scripts pass.
$install = Get-Content -Raw -LiteralPath (Join-Path $scriptDir 'install-endpoint.ps1')
foreach ($needle in @('endpoint', 'install', '--system', 'user-config', 'repair-installed')) {
    if ($install -notmatch [regex]::Escape($needle)) {
        Fail "install-endpoint.ps1 no longer references '$needle'"
    }
}
# BEACON_LOG_ACCESS is the one contract for the log access mode across all three platforms' scripts.
if ($install -notmatch "BEACON_LOG_ACCESS" -or $install -notmatch [regex]::Escape("'--log-access'")) {
    Fail "install-endpoint.ps1 no longer passes BEACON_LOG_ACCESS on as --log-access"
}
# The step whose absence made the Linux package produce a healthy collector that captured nothing.
if ($install -notmatch 'repair-installed') {
    Fail "install-endpoint.ps1 must configure the interactive user's runtime, or the endpoint captures nothing"
}

$uninstall = Get-Content -Raw -LiteralPath (Join-Path $scriptDir 'uninstall-endpoint.ps1')
foreach ($needle in @('--keep-logs', '--keep-config', 'BEACON_PURGE')) {
    if ($uninstall -notmatch [regex]::Escape($needle)) {
        Fail "uninstall-endpoint.ps1 no longer honors '$needle'; removing the product must not destroy configuration or logs by default"
    }
}

# Every beacon.exe call a package script makes is bounded and logged (package-common.ps1). A bare
# `& $beaconBin` would bring back the failure that is for: a call that never returns holds msiexec,
# and the fleet tool behind it, open with no record of which step it was.
foreach ($name in @('install-endpoint.ps1', 'uninstall-endpoint.ps1')) {
    $text = Get-Content -Raw -LiteralPath (Join-Path $scriptDir $name)
    $code = ($text -split "`n" | Where-Object { $_ -notmatch '^\s*#' }) -join "`n"
    if ($code -notmatch [regex]::Escape("package-common.ps1")) {
        Fail "$name no longer dot-sources package-common.ps1"
    }
    if ($code -notmatch 'Invoke-BeaconBounded') {
        Fail "$name no longer runs beacon.exe through Invoke-BeaconBounded"
    }
    if ($code -match '&\s*\$beaconBin') {
        Fail "$name runs beacon.exe directly; use Invoke-BeaconBounded so the call is bounded and logged"
    }
}

# The helpers themselves. Quoting decides whether a path with a space reaches beacon.exe as one
# argument, so the cases are spelled out: Go parses the command line by the same rules as the
# Microsoft C runtime.
. (Join-Path $scriptDir 'package-common.ps1')
$quoting = [ordered]@{
    'endpoint'                                       = 'endpoint'
    ''                                               = '""'
    'C:\Program Files\Beacon\bin\beacon-otelcol.exe' = '"C:\Program Files\Beacon\bin\beacon-otelcol.exe"'
    'C:\dir with space\'                             = '"C:\dir with space\\"'
    'say "hi"'                                       = '"say \"hi\""'
    'C:\a\"b'                                        = '"C:\a\\\"b"'
    'C:\no\spaces\here'                              = 'C:\no\spaces\here'
}
foreach ($entry in $quoting.GetEnumerator()) {
    $got = ConvertTo-BeaconArgument $entry.Key
    if ($got -ne $entry.Value) {
        Fail "ConvertTo-BeaconArgument '$($entry.Key)' gave '$got', want '$($entry.Value)'"
    }
}

# A call that outlives its bound is stopped, reported as a timeout and recorded, and an ordinary
# call returns its own exit code. Windows-only programs stand in for beacon.exe.
if ($IsWindows -or $env:OS -eq 'Windows_NT') {
    $cmd = Join-Path $env:SystemRoot 'System32\cmd.exe'
    Open-BeaconPackageLog 'selftest'
    $code = Invoke-BeaconBounded -Beacon $cmd -Arguments @('/d', '/c', 'exit 7') -TimeoutSeconds 30
    if ($code -ne 7) { Fail "Invoke-BeaconBounded returned $code for a call that exited 7" }
    $started = Get-Date
    $code = Invoke-BeaconBounded -Beacon $cmd -Arguments @('/d', '/c', 'ping -n 60 127.0.0.1 >nul') -TimeoutSeconds 3
    $elapsed = ((Get-Date) - $started).TotalSeconds
    if ($code -ne $BeaconTimeoutExitCode) { Fail "Invoke-BeaconBounded returned $code for a call past its bound, not $BeaconTimeoutExitCode" }
    if ($elapsed -gt 30) { Fail "Invoke-BeaconBounded took ${elapsed}s to give up on a 3s bound" }
    if (Get-CimInstance Win32_Process -Filter "Name = 'PING.EXE'" | Where-Object { $_.CommandLine -match '-n 60 127\.0\.0\.1' }) {
        Fail "Invoke-BeaconBounded left the timed-out call's child process running"
    }
    $code = Invoke-BeaconBounded -Beacon (Join-Path $env:SystemRoot 'System32\no-such-beacon.exe') -Arguments @('version') -TimeoutSeconds 30
    if ($code -ne $BeaconLaunchFailedExitCode) { Fail "Invoke-BeaconBounded returned $code for a program that could not start, not $BeaconLaunchFailedExitCode" }
    $logPath = $BeaconPackageLogPath
    Close-BeaconPackageLog
    if (-not $logPath -or -not (Test-Path -LiteralPath $logPath)) {
        Fail "Open-BeaconPackageLog created no log"
    } else {
        $logText = Get-Content -Raw -LiteralPath $logPath
        if ($logText -notmatch 'TIMEOUT:') { Fail "the package log does not record the timeout" }
        if ($logText -notmatch 'exit 7:') { Fail "the package log does not record the exit code" }
        if ($logText -notmatch 'could not start') { Fail "the package log does not record a failed start" }
        if (([regex]::Matches($logText, 'TIMEOUT:')).Count -ne 1) { Fail "the package log records a timeout that did not happen" }
        Remove-Item -LiteralPath $logPath -Force
        Write-Output 'ok: Invoke-BeaconBounded returns exit codes, stops a call past its bound, reports a failed start, and logs all three'
    }
}

# The ACL on %ProgramData%\Beacon belongs to `beacon endpoint install --system`, which gives ordinary
# users read and execute plus append to the runtime log, and nothing that lets them delete, rename or
# plant links. A packaging script that grants Modify or Full Control on top would quietly undo that.
foreach ($name in @('install-endpoint.ps1', 'uninstall-endpoint.ps1')) {
    $text = Get-Content -Raw -LiteralPath (Join-Path $scriptDir $name)
    $code = ($text -split "`n" | Where-Object { $_ -notmatch '^\s*#' }) -join "`n"
    if ($code -match '(?i)icacls[^\n]*/grant' -or $code -match '(?i)FileSystemAccessRule') {
        Fail "$name grants file system access itself; the ACL is applied by 'beacon endpoint install --system' and must not be widened here"
    }
}

# The WiX source has to keep running the scripts rather than growing its own copy of the flags.
$wxs = Get-Content -Raw -LiteralPath (Join-Path $scriptDir 'beacon.wxs')
foreach ($needle in @('install-endpoint.ps1', 'uninstall-endpoint.ps1')) {
    if ($wxs -notmatch [regex]::Escape($needle)) {
        Fail "beacon.wxs no longer invokes $needle"
    }
}
# Both scripts dot-source package-common.ps1 from their own directory, so the package has to ship it.
$build = Get-Content -Raw -LiteralPath (Join-Path $scriptDir 'build-msi.ps1')
if ($wxs -notmatch [regex]::Escape('scripts\package-common.ps1') -or $build -notmatch [regex]::Escape('package-common.ps1')) {
    Fail "the MSI no longer ships package-common.ps1 beside the scripts that dot-source it"
}
if ($wxs -match 'ExeCommand="[^"]*beacon\.exe') {
    Fail "beacon.wxs invokes beacon.exe directly; the install contract belongs in one place, not two"
}

if ($failures -gt 0) {
    Write-Output "$failures check(s) failed"
    exit 1
}
Write-Output 'Windows packaging scripts passed.'
