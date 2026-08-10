# Removes the Beacon endpoint on Windows.
#
# Called by the MSI's uninstall custom action, and usable directly for fleet tooling.
#
# Configuration and logs survive by default. That is the same retention contract the Debian packaging
# follows: removing the product should not destroy an operator's settings or the telemetry they have
# collected, because an uninstall is often the first half of an upgrade or a reinstall. Set
# BEACON_PURGE=1 to remove them, which is the deliberate, separate act.
#
# Keeping them takes more than passing --keep-config, and the flag's name is genuinely misleading:
# it governs *harness* telemetry settings, not Beacon's own. `endpoint uninstall` removes everything
# recorded in its install manifest -- which includes config.json and otelcol.yaml -- unconditionally,
# because that is what uninstall means at the CLI level. So the files are set aside and put back, the
# same stash-and-restore packaging/linux/nfpm/preremove.sh performs and for the same reason.
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

$configDir = Join-Path $env:ProgramData 'Beacon\Endpoint'
$retained = @('config.json', 'otelcol.yaml')

$stash = $null
if ($purge -ne '1') {
    $stash = Join-Path ([System.IO.Path]::GetTempPath()) ("beacon-uninstall-" + [Guid]::NewGuid().ToString('N'))
    New-Item -ItemType Directory -Force -Path $stash | Out-Null
    foreach ($name in $retained) {
        $source = Join-Path $configDir $name
        if (Test-Path -LiteralPath $source) {
            Copy-Item -LiteralPath $source -Destination (Join-Path $stash $name) -Force
        }
    }
    Write-Output "beacon: removing the endpoint, keeping configuration and logs (set BEACON_PURGE=1 to remove them)"
} else {
    Write-Output "beacon: removing the endpoint, configuration and logs"
}

$uninstallArgs = @('endpoint', 'uninstall', '--system')
if ($purge -ne '1') {
    # --keep-logs does what its name says. --keep-config is passed for the harness settings it does
    # govern; the stash above is what actually preserves Beacon's own configuration.
    $uninstallArgs += @('--keep-logs', '--keep-config')
}

& $beaconBin @uninstallArgs
if ($LASTEXITCODE -ne 0) {
    # Reported, not thrown. The service and its registration are what matter here, and `endpoint
    # uninstall` is idempotent -- so a non-zero exit is worth surfacing but not worth wedging an MSI
    # removal over. A user who cannot uninstall the package has a worse problem than a leftover file.
    Write-Warning "beacon: endpoint uninstall exited $LASTEXITCODE; check 'beacon endpoint status --system'"
}

if ($null -ne $stash) {
    try {
        $restored = 0
        foreach ($name in $retained) {
            $saved = Join-Path $stash $name
            if (Test-Path -LiteralPath $saved) {
                New-Item -ItemType Directory -Force -Path $configDir | Out-Null
                Copy-Item -LiteralPath $saved -Destination (Join-Path $configDir $name) -Force
                $restored++
            }
        }
        if ($restored -gt 0) {
            Write-Output "beacon: restored $restored configuration file(s) to $configDir"
        }
    } finally {
        Remove-Item -LiteralPath $stash -Recurse -Force -ErrorAction SilentlyContinue
    }
} else {
    # The purge half of the contract, matching postremove.sh: what was deliberately kept above is
    # exactly what a purge is for removing. One recursive delete covers the logs too, because the
    # system log directory lives under this one on Windows -- there is no /var/log equivalent to
    # remove separately.
    Remove-Item -LiteralPath $configDir -Recurse -Force -ErrorAction SilentlyContinue
}

Write-Output "beacon: endpoint removed"
