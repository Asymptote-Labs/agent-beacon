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

$scripts = @('install-endpoint.ps1', 'uninstall-endpoint.ps1', 'build-msi.ps1', 'sign-authenticode.ps1', 'assert-authenticode.ps1')
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
if ($wxs -match 'ExeCommand="[^"]*beacon\.exe') {
    Fail "beacon.wxs invokes beacon.exe directly; the install contract belongs in one place, not two"
}

# Without signing configured a release must still ship, unsigned, exactly as before.
$probe = Join-Path ([System.IO.Path]::GetTempPath()) ("beacon-sign-probe-" + [Guid]::NewGuid().ToString('N') + '.ps1')
"Write-Output 'unsigned'" | Set-Content -LiteralPath $probe -Encoding ascii
try {
    $before = (Get-FileHash -LiteralPath $probe).Hash
    $env:AZURE_TRUSTED_SIGNING_ACCOUNT = $null
    & pwsh -NoProfile -File (Join-Path $scriptDir 'sign-authenticode.ps1') -Path $probe | Out-Null
    if ($LASTEXITCODE -ne 0) { Fail "sign-authenticode.ps1 fails when signing is not configured; an unsigned release must still succeed" }
    elseif ((Get-FileHash -LiteralPath $probe).Hash -ne $before) { Fail "sign-authenticode.ps1 changed a file when signing is not configured" }
    else { Write-Output 'ok: sign-authenticode.ps1 leaves files alone when signing is not configured' }

    if ($IsWindows) {
        $assert = Join-Path $scriptDir 'assert-authenticode.ps1'
        & pwsh -NoProfile -File $assert -Path $probe | Out-Null
        if ($LASTEXITCODE -eq 0) { Fail "assert-authenticode.ps1 accepted an unsigned file" }
        # pwsh itself is validly signed, by Microsoft: refused for the wrong publisher, accepted for
        # the right one, so a pass is not just a check that always fails.
        $pwsh = (Get-Process -Id $PID).Path
        & pwsh -NoProfile -File $assert -Path $pwsh | Out-Null
        if ($LASTEXITCODE -eq 0) { Fail "assert-authenticode.ps1 accepted a file signed by another publisher" }
        & pwsh -NoProfile -File $assert -Path $pwsh -Publisher 'Microsoft Corporation'
        if ($LASTEXITCODE -ne 0) { Fail "assert-authenticode.ps1 refused a validly signed file" }
    }
} finally {
    Remove-Item -LiteralPath $probe -Force -ErrorAction SilentlyContinue
}

if ($failures -gt 0) {
    Write-Output "$failures check(s) failed"
    exit 1
}
Write-Output 'Windows packaging scripts passed.'
