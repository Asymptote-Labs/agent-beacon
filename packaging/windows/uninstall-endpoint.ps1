# Removes the Beacon endpoint on Windows.
#
# Called by the MSI's uninstall custom action, and usable directly for fleet tooling.
#
# Configuration and logs survive by default. That is the same retention contract the Debian packaging
# follows: removing the product should not destroy an operator's settings or the telemetry they have
# collected, because an uninstall is often the first half of an upgrade or a reinstall. Set
# BEACON_PURGE=1 to remove them, which is the deliberate, separate act.
[CmdletBinding()]
param()

$ErrorActionPreference = 'Stop'

function Get-EnvOrDefault {
    param([string]$Name, [string]$Default)
    $value = [Environment]::GetEnvironmentVariable($Name)
    if ([string]::IsNullOrWhiteSpace($value)) { return $Default }
    return $value
}

$installRoot = Get-EnvOrDefault 'BEACON_INSTALL_ROOT' (Join-Path $env:ProgramFiles 'Beacon')
$beaconBin = Get-EnvOrDefault 'BEACON_BIN' (Join-Path $installRoot 'bin\beacon.exe')
$purge = Get-EnvOrDefault 'BEACON_PURGE' '0'

if (-not (Test-Path -LiteralPath $beaconBin)) {
    # Nothing to do, and not an error. An uninstall must not fail because the thing it removes is
    # already gone -- a failed uninstall custom action leaves the MSI half-removed, which is a worse
    # state than either finishing or never starting.
    Write-Output "beacon: $beaconBin is not present; nothing to uninstall"
    exit 0
}

$uninstallArgs = @('endpoint', 'uninstall', '--system')
if ($purge -ne '1') {
    $uninstallArgs += @('--keep-logs', '--keep-config')
    Write-Output "beacon: removing the endpoint, keeping configuration and logs (set BEACON_PURGE=1 to remove them)"
} else {
    Write-Output "beacon: removing the endpoint, configuration and logs"
}

& $beaconBin @uninstallArgs
if ($LASTEXITCODE -ne 0) {
    # Reported, not thrown. The service and its registration are what matter here, and `endpoint
    # uninstall` is idempotent -- so a non-zero exit is worth surfacing but not worth wedging an MSI
    # removal over. A user who cannot uninstall the package has a worse problem than a leftover file.
    Write-Warning "beacon: endpoint uninstall exited $LASTEXITCODE; check 'beacon endpoint status --system'"
}

Write-Output "beacon: endpoint removed"
