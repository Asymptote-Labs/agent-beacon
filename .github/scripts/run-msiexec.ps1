# Runs msiexec with a bound on how long it may take, for CI.
#
# `Start-Process -Wait` waits forever, and an installer that wedges is not hypothetical: an msiexec
# that cannot stop a service, or that is blocked behind another installation, sits there. What the
# job then produces is a runner held until the job timeout expires and a red X with no log in it --
# the verbose log is written by msiexec but never read, because the step that would have printed it
# never returned.
#
# So the wait is bounded and the log is printed either way. A timeout here is still a failure; the
# difference is that it is a failure someone can read, minutes rather than tens of minutes after it
# started.
[CmdletBinding()]
param(
    # What this invocation is, for the failure message: "install", "upgrade", "uninstall".
    [Parameter(Mandatory = $true)][string]$What,
    [Parameter(Mandatory = $true)][string[]]$Arguments,
    [Parameter(Mandatory = $true)][string]$LogPath,
    # Generous by an order of magnitude. A healthy install, upgrade or removal of this package
    # finishes in single-digit seconds, so this bounds a hang without being a flake risk of its own.
    [int]$TimeoutSeconds = 180,
    [int]$LogTailLines = 150
)

$ErrorActionPreference = 'Stop'

function Show-MsiLog {
    if (-not (Test-Path -LiteralPath $LogPath)) {
        Write-Output "--- no $What log at $LogPath ---"
        return
    }
    Write-Output "--- $What log (last $LogTailLines lines) ---"
    Get-Content -LiteralPath $LogPath -Tail $LogTailLines | Write-Output
    Write-Output "--- end of $What log ---"
}

$process = Start-Process msiexec.exe -PassThru -ArgumentList (@('/l*v', $LogPath) + $Arguments)

# Touching .Handle caches it, which is what makes .ExitCode readable afterwards. Without it
# Start-Process -PassThru hands back an object whose ExitCode is null once the process has gone.
$null = $process.Handle

if (-not $process.WaitForExit($TimeoutSeconds * 1000)) {
    Show-MsiLog
    # Kills the client. The install service may still be finishing on its own, which is why this
    # throws rather than carrying on to assert anything about the machine's state -- after a timeout
    # the machine is mid-transaction and nothing observed about it would mean much.
    try { $process.Kill() } catch { Write-Output "could not kill msiexec: $_" }
    throw "msiexec $What did not finish within $TimeoutSeconds seconds"
}

if ($process.ExitCode -ne 0) {
    Show-MsiLog
    throw "msiexec $What exited $($process.ExitCode)"
}

Write-Output "msiexec $What completed"
