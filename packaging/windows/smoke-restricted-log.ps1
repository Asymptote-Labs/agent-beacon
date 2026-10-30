# End-to-end check of the restricted log access mode on an installed Windows endpoint.
#
# Runs after the MSI is installed and its BeaconCollector service is running. It needs an elevated
# shell, which the CI runner is, because it switches the endpoint's mode and creates a local account.
#
# What it proves, with a real ordinary account rather than the elevated runner's own:
#   - `beacon endpoint repair --system --log-access restricted` restricts the log, and status and
#     doctor agree that it is.
#   - That account's hook reaches the log through the collector's pipe, under the account's own SID
#     and name whatever the event claimed, and the account cannot read or append to the log itself.
#   - While the collector is stopped the hook spools its event instead of losing it, and the event
#     arrives with the next one once the collector is back.
#   - `--log-access shared` puts it back: the hook appends directly again and doctor is satisfied.
[CmdletBinding()]
param()

$ErrorActionPreference = 'Stop'

$beacon = Join-Path $env:ProgramFiles 'Beacon\bin\beacon.exe'
$hooks = Join-Path $env:ProgramFiles 'Beacon\bin\beacon-hooks.exe'
$log = Join-Path $env:ProgramData 'Beacon\Endpoint\logs\runtime.jsonl'
$config = Join-Path $env:ProgramData 'Beacon\Endpoint\config.json'
$pipe = '\\.\pipe\beacon-agent-ingest'
$work = Join-Path $env:SystemDrive 'BeaconSmoke'
$account = 'beaconsmoke'

function Invoke-Beacon {
    param([string[]]$Arguments)
    $out = & $beacon @Arguments 2>&1 | Out-String
    if ($LASTEXITCODE -ne 0) {
        Write-Output $out
        throw "beacon $($Arguments -join ' ') exited $LASTEXITCODE"
    }
    return $out
}

function Get-Status {
    return (Invoke-Beacon @('endpoint', 'status', '--system', '--json')) | ConvertFrom-Json
}

function Get-Check {
    param($Status, [string]$Name)
    return $Status.diagnostics | Where-Object { $_.name -eq $Name } | Select-Object -First 1
}

function Test-Pipe {
    return [System.IO.Directory]::GetFiles('\\.\pipe\') -contains $pipe
}

function Wait-Pipe {
    param([bool]$Present)
    $deadline = (Get-Date).AddSeconds(60)
    while ((Test-Pipe) -ne $Present) {
        if ((Get-Date) -gt $deadline) { throw "the ingest pipe never became $(if ($Present) { 'available' } else { 'absent' })" }
        Start-Sleep -Milliseconds 500
    }
}

# Every log line holding the marker, parsed. Read as this elevated account, which may read the log in
# either mode.
function Get-LogEvents {
    param([string]$Marker)
    if (-not (Test-Path -LiteralPath $log)) { return @() }
    return @(Get-Content -LiteralPath $log | Where-Object { $_.Contains($Marker) } | ForEach-Object { $_ | ConvertFrom-Json })
}

# Fourteen characters at most: net.exe stops to ask "continue?" for a longer password, and with
# no answer on stdin it fails without creating the account.
$password = 'Bs1!' + [Guid]::NewGuid().ToString('N').Substring(0, 10)
$credential = [System.Management.Automation.PSCredential]::new(
    $account, (ConvertTo-SecureString $password -AsPlainText -Force))

# Runs a batch file as the ordinary account and returns its exit code. A batch file because the
# environment a hook gets from its runtime is set inline there, and Start-Process cannot give a
# process started under other credentials an environment of the caller's choosing.
function Invoke-AsAccount {
    param([string]$Tag, [string[]]$Lines)
    $script = Join-Path $work "$Tag.cmd"
    Set-Content -LiteralPath $script -Encoding ascii -Value (@('@echo off') + $Lines + @('exit /b %ERRORLEVEL%'))
    $p = Start-Process -FilePath (Join-Path $env:SystemRoot 'System32\cmd.exe') -ArgumentList @('/d', '/c', "`"$script`"") `
        -Credential $credential -LoadUserProfile -WorkingDirectory $work -Wait -PassThru
    return $p.ExitCode
}

# Runs the Claude Code SessionStart hook as the ordinary account, the way Claude Code would. USER is
# set to an account it is not, which a shared-mode hook would write into user.name as it stands.
function Invoke-Hook {
    param([string]$Session)
    $payload = Join-Path $work "$Session.json"
    @{ session_id = $Session; hook_event_name = 'SessionStart'; source = 'startup'; cwd = $work } |
        ConvertTo-Json | Set-Content -LiteralPath $payload -Encoding utf8
    $code = Invoke-AsAccount $Session @(
        'set "BEACON_ENDPOINT_MODE=1"',
        "set `"BEACON_ENDPOINT_LOG=$log`"",
        "set `"BEACON_ENDPOINT_CONFIG=$config`"",
        'set "USER=Administrator"',
        "echo %USERPROFILE%> `"$work\$Session.home`"",
        "`"$hooks`" --platform claude session-start < `"$payload`" > `"$work\$Session.out`" 2> `"$work\$Session.err`""
    )
    if ($code -ne 0) {
        Get-Content -LiteralPath "$work\$Session.err" -ErrorAction SilentlyContinue | Write-Output
        throw "the hook for $Session exited $code"
    }
}

New-Item -ItemType Directory -Force -Path $work | Out-Null
& net.exe user $account $password /add | Out-Null
if ($LASTEXITCODE -ne 0) { throw "could not create the local account $account" }
try {
    & icacls.exe $work /grant "${account}:(OI)(CI)M" | Out-Null
    $sid = ([System.Security.Principal.NTAccount]"$env:COMPUTERNAME\$account").Translate(
        [System.Security.Principal.SecurityIdentifier]).Value
    Write-Output "ordinary account: $account ($sid)"

    # --- Restricted ----------------------------------------------------------------------------
    Invoke-Beacon @('endpoint', 'repair', '--system', '--log-access', 'restricted') | Write-Output
    Wait-Pipe $true
    $status = Get-Status
    if ($status.log_access -ne 'restricted') { throw "status reports log_access '$($status.log_access)' after restricting" }
    foreach ($name in @('runtime_log_access', 'runtime_log_permissions')) {
        $check = Get-Check $status $name
        if (-not $check -or $check.status -ne 'ok') {
            $status.diagnostics | ConvertTo-Json -Depth 5 | Write-Output
            throw "doctor's $name is not ok on the restricted endpoint"
        }
    }

    $sessionA = 'smoke-a-' + [Guid]::NewGuid().ToString('N')
    Invoke-Hook $sessionA
    $events = Get-LogEvents $sessionA
    if ($events.Count -lt 1) { throw "the ordinary account's event never reached the restricted log" }
    foreach ($e in $events) {
        if ($e.user.uid -ne $sid) { throw "user.uid is '$($e.user.uid)'; the collector must stamp the sender's SID $sid" }
        if ($e.user.name -notlike "*\$account") { throw "user.name is '$($e.user.name)'; the event claimed Administrator and must carry $account" }
    }
    Write-Output "ok: the hook's event reached the log through the pipe as $($events[0].user.name)"

    # `|| exit /b 1` rather than the exit code alone: cmd leaves ERRORLEVEL at 0 when a redirection
    # (the append's `>>`) cannot open its file, and only `||` sees that failure.
    if ((Invoke-AsAccount 'read' @("type `"$log`" > nul 2>&1 || exit /b 1", 'exit /b 0')) -eq 0) { throw "an ordinary account can read the restricted log" }
    $forged = 'forged-' + [Guid]::NewGuid().ToString('N')
    if ((Invoke-AsAccount 'append' @("(echo $forged>> `"$log`") 2>nul || exit /b 1", 'exit /b 0')) -eq 0 -or (Get-LogEvents $forged).Count -gt 0) {
        throw "an ordinary account can append to the restricted log"
    }
    Write-Output "ok: the ordinary account can neither read nor append to the log"

    # Collector down: the event waits in the account's spool, and arrives with the next one.
    Stop-Service -Name BeaconCollector
    Wait-Pipe $false
    $sessionB = 'smoke-b-' + [Guid]::NewGuid().ToString('N')
    Invoke-Hook $sessionB
    if ((Get-LogEvents $sessionB).Count -gt 0) { throw "an event reached the restricted log with the collector stopped" }
    # The profile the hook ran under, as it saw it: Windows names a profile directory after the
    # account only when that name is free.
    $profileDir = (Get-Content -LiteralPath "$work\$sessionB.home" -ErrorAction SilentlyContinue | Select-Object -First 1)
    if (-not $profileDir) { $profileDir = Join-Path $env:SystemDrive "Users\$account" }
    $spool = Join-Path $profileDir.Trim() '.beacon\endpoint\spool'
    if (-not (Get-ChildItem -LiteralPath $spool -Recurse -File -ErrorAction SilentlyContinue | Select-String -SimpleMatch $sessionB -Quiet)) {
        Write-Output "--- hook output for $sessionB ---"
        Get-Content -LiteralPath "$work\$sessionB.out", "$work\$sessionB.err" -ErrorAction SilentlyContinue | Write-Output
        Write-Output "--- files under $profileDir\.beacon ---"
        Get-ChildItem -LiteralPath (Join-Path $profileDir.Trim() '.beacon') -Recurse -Force -ErrorAction Continue 2>&1 |
            ForEach-Object { "$($_.FullName) $($_.Length)" } | Write-Output
        throw "the event sent while the collector was stopped is not in the account's spool ($spool)"
    }
    Start-Service -Name BeaconCollector
    Wait-Pipe $true
    $sessionC = 'smoke-c-' + [Guid]::NewGuid().ToString('N')
    Invoke-Hook $sessionC
    foreach ($session in @($sessionB, $sessionC)) {
        if ((Get-LogEvents $session).Count -lt 1) { throw "$session did not reach the log once the collector was back" }
    }
    Write-Output "ok: an event spooled while the collector was stopped arrived once it was back"

    # --- Shared again -----------------------------------------------------------------------------
    Invoke-Beacon @('endpoint', 'repair', '--system', '--log-access', 'shared') | Write-Output
    Wait-Pipe $false
    $status = Get-Status
    if ($status.log_access -ne 'shared') { throw "status reports log_access '$($status.log_access)' after sharing" }
    $permissions = Get-Check $status 'runtime_log_permissions'
    if (-not $permissions -or $permissions.status -ne 'ok') {
        $status.diagnostics | ConvertTo-Json -Depth 5 | Write-Output
        throw "doctor's runtime_log_permissions is not ok once the log is shared again"
    }
    $sessionD = 'smoke-d-' + [Guid]::NewGuid().ToString('N')
    Invoke-Hook $sessionD
    if ((Get-LogEvents $sessionD).Count -lt 1) { throw "the hook could not append to the log once it was shared again" }
    Write-Output "ok: shared again, the hook appends directly"
} catch {
    Write-Output "--- runtime log (tail) ---"
    Get-Content -LiteralPath $log -Tail 20 -ErrorAction SilentlyContinue | Write-Output
    throw
} finally {
    & net.exe user $account /delete | Out-Null
    $global:LASTEXITCODE = 0
}
