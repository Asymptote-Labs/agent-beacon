# Installs the Beacon endpoint in system mode on Windows.
#
# Called by the MSI's install custom action, and usable directly for fleet tooling (Intune, SCCM, a
# login script). Configuration is environment-first, matching packaging/linux/install-endpoint.sh and
# packaging/macos/install-endpoint.sh so a mixed fleet has one contract rather than three.
#
# There is deliberately only one definition of what a system install means. The MSI runs this script
# rather than invoking beacon.exe with its own flag list, exactly as the .deb and .rpm postinstall
# scripts call install-endpoint.sh -- two lists of flags that must agree is a thing that stops
# agreeing.
[CmdletBinding()]
param()

$ErrorActionPreference = 'Stop'

function Get-EnvOrDefault {
    param([string]$Name, [string]$Default)
    $value = [Environment]::GetEnvironmentVariable($Name)
    if ([string]::IsNullOrWhiteSpace($value)) { return $Default }
    return $value
}

# The installed layout, which mirrors /opt/beacon: bin next to scripts, so the collector sits beside
# the CLI where DiscoverBinary looks for it first.
$installRoot = Get-EnvOrDefault 'BEACON_INSTALL_ROOT' (Join-Path $env:ProgramFiles 'Beacon')
$beaconBin = Get-EnvOrDefault 'BEACON_BIN' (Join-Path $installRoot 'bin\beacon.exe')

if (-not (Test-Path -LiteralPath $beaconBin)) {
    throw "beacon.exe was not found at $beaconBin"
}

$harnesses = Get-EnvOrDefault 'BEACON_ENDPOINT_HARNESSES' 'claude,codex'
$grpcPort = Get-EnvOrDefault 'BEACON_OTLP_GRPC_PORT' '4317'
$httpPort = Get-EnvOrDefault 'BEACON_OTLP_HTTP_PORT' '4318'
$collector = Get-EnvOrDefault 'BEACON_COLLECTOR' ''
$service = Get-EnvOrDefault 'BEACON_SERVICE' ''
$noStart = Get-EnvOrDefault 'BEACON_NO_START' '0'

# Auto-detected rather than required, so a manual invocation without BEACON_COLLECTOR still works.
# DiscoverBinary would find this on its own -- it is passed explicitly so that a failure says which
# collector was expected instead of "no collector found".
if ([string]::IsNullOrWhiteSpace($collector)) {
    $packaged = Join-Path $installRoot 'bin\beacon-otelcol.exe'
    if (Test-Path -LiteralPath $packaged) { $collector = $packaged }
}

$installArgs = @(
    'endpoint', 'install', '--system',
    '--harness', $harnesses,
    '--otlp-grpc-port', $grpcPort,
    '--otlp-http-port', $httpPort
)
if (-not [string]::IsNullOrWhiteSpace($collector)) { $installArgs += @('--collector', $collector) }
# Empty means auto-detect, which on Windows resolves to the Service Control Manager for a system
# install. Passing it explicitly is for a fleet that wants the supervised fallback instead.
if (-not [string]::IsNullOrWhiteSpace($service)) { $installArgs += @('--service', $service) }
if ($noStart -eq '1') { $installArgs += '--no-start' }

Write-Output "beacon: installing the system endpoint"
& $beaconBin @installArgs
if ($LASTEXITCODE -ne 0) {
    throw "beacon endpoint install failed with exit code $LASTEXITCODE"
}

# A system endpoint runs elevated, and the install above configured *this* account's Claude Code and
# Codex settings. For an MSI that account is whoever ran the installer, or SYSTEM when a management
# tool deployed it -- neither of which is necessarily the person at the keyboard. This second step
# resolves that person and configures their runtime.
#
# Without it the collector runs perfectly and captures nothing anyone cares about. That is not
# hypothetical: it is exactly what the Linux package did before the equivalent step was added.
#
# Non-fatal, and it says why when it cannot. An unattended install may have no interactive user at
# all, which is a legitimate outcome rather than a failure.
Write-Output "beacon: configuring the interactive user's agent runtime"
& $beaconBin endpoint user-config repair-installed --system --harness $harnesses
if ($LASTEXITCODE -ne 0) {
    Write-Warning ("beacon: could not configure the interactive user's agent runtime; " +
        "run 'beacon endpoint user-config repair-installed --system' once logged in")
}

Write-Output "beacon: endpoint installed. Check it with: beacon endpoint status --system"
