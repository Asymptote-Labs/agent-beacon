# How the Windows package scripts run beacon.exe. Dot-sourced by install-endpoint.ps1 and
# uninstall-endpoint.ps1, so the two cannot drift apart on it.
#
# An MSI custom action has no console and no time limit of its own. A beacon.exe call that never
# returns therefore holds the whole installation open: msiexec waits on the action, a fleet tool
# (Intune, Workspace ONE, SCCM) waits on msiexec, and nothing records which step it was. CI saw
# exactly that, intermittently, in both an upgrade and an uninstall, and each time the job was
# cancelled at its time limit with its log discarded. So every call made here is bounded, and every
# call leaves a record.
#
# The record is a file of its own in the temporary directory of the account running the script --
# SYSTEM, for the MSI -- because nothing else survives: Windows Installer keeps no output from an
# executable custom action, and an uninstall removes the payload directory. The file name is new for
# each run and the file is created rather than opened, so a file an ordinary user planted under that
# name in advance can never receive SYSTEM's writes. The log is diagnostics only: if it cannot be
# created, the script carries on without it.

# ERROR_TIMEOUT. Returned by Invoke-BeaconBounded for a call it had to stop, so a caller can tell
# "beacon.exe failed" from "beacon.exe never finished".
$script:BeaconTimeoutExitCode = 1460

$script:BeaconPackageLog = $null
$script:BeaconPackageLogPath = $null

function Open-BeaconPackageLog {
    param([string]$Action)
    $name = 'beacon-package-{0}-{1:yyyyMMddTHHmmss}-{2}.log' -f $Action, (Get-Date), ([Guid]::NewGuid().ToString('N').Substring(0, 8))
    $path = Join-Path ([System.IO.Path]::GetTempPath()) $name
    try {
        $stream = New-Object System.IO.FileStream($path, [System.IO.FileMode]::CreateNew, [System.IO.FileAccess]::Write, [System.IO.FileShare]::Read)
        $writer = New-Object System.IO.StreamWriter($stream, (New-Object System.Text.UTF8Encoding($false)))
        $writer.AutoFlush = $true
        $script:BeaconPackageLog = $writer
        $script:BeaconPackageLogPath = $path
    } catch {
        $script:BeaconPackageLog = $null
        $script:BeaconPackageLogPath = $null
    }
}

# Written to the console as well as the log, through [Console] rather than Write-Output: Write-Output
# inside a function becomes part of that function's return value, which would corrupt the exit code
# Invoke-BeaconBounded returns.
function Write-BeaconPackageLog {
    param([string]$Message)
    $line = '{0:o} {1}' -f (Get-Date), $Message
    [Console]::Out.WriteLine($line)
    if ($null -ne $script:BeaconPackageLog) {
        try { $script:BeaconPackageLog.WriteLine($line) } catch { $script:BeaconPackageLog = $null }
    }
}

function Close-BeaconPackageLog {
    if ($null -ne $script:BeaconPackageLog) {
        try { $script:BeaconPackageLog.Dispose() } catch { }
        $script:BeaconPackageLog = $null
    }
}

# Quotes one argument for the command line a Windows program parses back into argv, which is how
# Go's runtime reads it. Windows PowerShell 5.1 runs on .NET Framework, whose ProcessStartInfo takes
# one command-line string rather than an argument list, and the paths passed here contain spaces
# (C:\Program Files\...). Backslashes are literal except in a run that ends at a quote, where each
# one has to be doubled -- which includes a run at the very end, followed by the closing quote.
function ConvertTo-BeaconArgument {
    param([string]$Value)
    if ($Value.Length -gt 0 -and $Value -notmatch '[\s"]') { return $Value }
    $escaped = [regex]::Replace($Value, '(\\*)"', { param($m) $m.Groups[1].Value + $m.Groups[1].Value + '\"' })
    $escaped = [regex]::Replace($escaped, '(\\+)$', { param($m) $m.Groups[1].Value + $m.Groups[1].Value })
    return '"' + $escaped + '"'
}

function Write-BeaconStream {
    param([string]$Name, $Task)
    # The pipes close once every process holding them has exited, which the stop below guarantees
    # for beacon's own process tree. A reader is still bounded, because a process outside that tree
    # could have inherited one.
    try {
        if (-not $Task.Wait(5000)) {
            Write-BeaconPackageLog "  ($Name did not close)"
            return
        }
        $text = $Task.Result
    } catch {
        Write-BeaconPackageLog "  ($Name could not be read: $($_.Exception.Message))"
        return
    }
    foreach ($line in ($text -split "`r?`n")) {
        if ($line.Length -gt 0) { Write-BeaconPackageLog "  $Name> $line" }
    }
}

# Runs beacon.exe with Arguments, waits at most TimeoutSeconds, and returns its exit code, or
# $BeaconTimeoutExitCode once it had to be stopped. Output is recorded line by line in the package
# log.
#
# A beacon.exe still running fifteen seconds before the bound writes every goroutine's stack to
# stderr (BEACON_DEBUG_STACKS_AFTER), so a call that hangs names the code it is stuck in, not just
# the command.
function Invoke-BeaconBounded {
    param([string]$Beacon, [string[]]$Arguments, [int]$TimeoutSeconds)

    # Windows PowerShell turns a native command's stderr into a terminating error under the callers'
    # 'Stop' preference once it is redirected, and taskkill reports a child that already exited on
    # stderr. Stopping a hung call must never be what fails the install or uninstall.
    $ErrorActionPreference = 'Continue'

    $display = 'beacon ' + ($Arguments -join ' ')
    Write-BeaconPackageLog "running: $display (bound ${TimeoutSeconds}s)"

    $info = New-Object System.Diagnostics.ProcessStartInfo
    $info.FileName = $Beacon
    $info.Arguments = (@($Arguments | ForEach-Object { ConvertTo-BeaconArgument $_ })) -join ' '
    $info.UseShellExecute = $false
    $info.CreateNoWindow = $true
    $info.RedirectStandardOutput = $true
    $info.RedirectStandardError = $true
    $info.StandardOutputEncoding = [System.Text.Encoding]::UTF8
    $info.StandardErrorEncoding = [System.Text.Encoding]::UTF8
    $info.EnvironmentVariables['BEACON_DEBUG_STACKS_AFTER'] = "$([Math]::Max(1, $TimeoutSeconds - 15))s"

    $process = [System.Diagnostics.Process]::Start($info)
    # Both streams are drained concurrently. Reading one to the end before the other deadlocks as
    # soon as beacon fills the pipe nobody is reading.
    $stdout = $process.StandardOutput.ReadToEndAsync()
    $stderr = $process.StandardError.ReadToEndAsync()

    $exited = $process.WaitForExit($TimeoutSeconds * 1000)
    if (-not $exited) {
        Write-BeaconPackageLog "TIMEOUT: $display did not finish within ${TimeoutSeconds}s; stopping it and its child processes"
        & taskkill.exe /T /F /PID $process.Id 2>&1 | ForEach-Object { Write-BeaconPackageLog "  $_" }
        $global:LASTEXITCODE = 0
        $null = $process.WaitForExit(10000)
    }
    Write-BeaconStream 'stdout' $stdout
    Write-BeaconStream 'stderr' $stderr

    if (-not $exited) { return $script:BeaconTimeoutExitCode }
    Write-BeaconPackageLog "exit $($process.ExitCode): $display"
    return $process.ExitCode
}
