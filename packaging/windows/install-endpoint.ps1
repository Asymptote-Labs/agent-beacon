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

. (Join-Path $PSScriptRoot 'package-common.ps1')
Open-BeaconPackageLog 'install'

# Bounds for the two calls below, generous because a slow machine must not fail an install: the first
# install starts the service, waits for the collector to answer, configures runtimes and reads up to
# 30 days of local session history. They exist so that a call that never returns fails the install
# with a record of where, instead of holding msiexec and the fleet tool behind it open indefinitely.
$installTimeoutSeconds = 600
$userConfigTimeoutSeconds = 300

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
    Write-BeaconPackageLog "beacon.exe was not found at $beaconBin"
    throw "beacon.exe was not found at $beaconBin"
}

$harnesses = Get-EnvOrDefault 'BEACON_ENDPOINT_HARNESSES' 'claude,codex'
$grpcPort = Get-EnvOrDefault 'BEACON_OTLP_GRPC_PORT' '4317'
$httpPort = Get-EnvOrDefault 'BEACON_OTLP_HTTP_PORT' '4318'
$collector = Get-EnvOrDefault 'BEACON_COLLECTOR' ''
$service = Get-EnvOrDefault 'BEACON_SERVICE' ''
$noStart = Get-EnvOrDefault 'BEACON_NO_START' '0'
# shared or restricted; empty keeps whatever the endpoint already has, so an upgrade never changes
# the mode. Restricted makes the SYSTEM collector the only writer of the runtime log: hooks send their
# events to it over a local named pipe, and only SYSTEM, Administrators and the beacon-log group can
# read the log. The same variable the Linux and macOS install scripts read.
$logAccess = Get-EnvOrDefault 'BEACON_LOG_ACCESS' ''

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
if (-not [string]::IsNullOrWhiteSpace($logAccess)) { $installArgs += @('--log-access', $logAccess) }

# The access control on %ProgramData%\Beacon is applied by the install itself, not here, so a fleet
# that runs `beacon endpoint install --system` directly gets the same result as the MSI. It resets the
# directory even when it already exists -- %ProgramData% lets any user create a subdirectory, so one
# found there may belong to someone else -- to Administrators ownership with inheritance disabled:
# SYSTEM and Administrators full control, Users read and execute. The log directory adds only an
# inherit-only grant letting INTERACTIVE append to the runtime log files; ordinary users cannot add,
# delete or rename anything in it. In the restricted log access mode the log directory grants
# ordinary users nothing at all. Do not add an icacls grant here: a second, broader grant is how
# the log directory once became writable enough to redirect the collector's writes.
Write-BeaconPackageLog "beacon: installing the system endpoint"
$code = Invoke-BeaconBounded -Beacon $beaconBin -Arguments $installArgs -TimeoutSeconds $installTimeoutSeconds
if ($code -ne 0) {
    Write-BeaconPackageLog "beacon endpoint install failed with exit code $code; log: $script:BeaconPackageLogPath"
    Close-BeaconPackageLog
    throw "beacon endpoint install failed with exit code $code"
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
Write-BeaconPackageLog "beacon: configuring the interactive user's agent runtime"
$code = Invoke-BeaconBounded -Beacon $beaconBin -Arguments @('endpoint', 'user-config', 'repair-installed', '--system', '--harness', $harnesses) -TimeoutSeconds $userConfigTimeoutSeconds
if ($code -ne 0) {
    Write-BeaconPackageLog ("beacon: could not configure the interactive user's agent runtime (exit $code); " +
        "run 'beacon endpoint user-config repair-installed --system' once logged in")
}

Write-BeaconPackageLog "beacon: endpoint installed. Check it with: beacon endpoint status --system"
Close-BeaconPackageLog
