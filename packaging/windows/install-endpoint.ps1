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
param(
    # The MSI's UILevel property (2 none, 3 basic, 4 reduced, 5 full), passed by its custom action so
    # this script can tell a person running the installer from a silent fleet deployment. Empty when
    # fleet tooling runs the script directly, which counts as unattended.
    [string]$UiLevel = ''
)

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

# The install settings fleet tooling (Intune, Group Policy, SCCM) sets as registry policy: values
# named exactly like the BEACON_* variables the Linux and macOS install scripts read, under this key.
# The Windows counterpart of /etc/default/beacon on Linux and the com.beacon.endpoint managed
# preferences on macOS, because an MSI deployment cannot set the environment of its custom action.
# HKLM\SOFTWARE\Policies is writable only by administrators. BEACON_POLICY_KEY lets the packaging
# tests point elsewhere.
$policyKey = Get-EnvOrDefault 'BEACON_POLICY_KEY' 'HKLM:\SOFTWARE\Policies\Beacon\Endpoint'

# Test-BeaconPolicy reports whether the policy key sets a value, even an empty one.
function Test-BeaconPolicy {
    param([string]$Name)
    if (-not (Test-Path -LiteralPath $policyKey)) { return $false }
    $item = Get-ItemProperty -LiteralPath $policyKey -Name $Name -ErrorAction SilentlyContinue
    return $null -ne $item
}

# Get-BeaconSetting reads a documented install setting: the environment wins, then the policy key,
# then the default.
function Get-BeaconSetting {
    param([string]$Name, [string]$Default)
    $value = [Environment]::GetEnvironmentVariable($Name)
    if (-not [string]::IsNullOrWhiteSpace($value)) { return $value }
    if (Test-BeaconPolicy $Name) {
        $policy = [string](Get-ItemProperty -LiteralPath $policyKey -Name $Name).$Name
        if (-not [string]::IsNullOrWhiteSpace($policy)) { return $policy.Trim() }
    }
    return $Default
}

# The installed layout, which mirrors /opt/beacon: bin next to scripts, so the collector sits beside
# the CLI where DiscoverBinary looks for it first.
$installRoot = Get-EnvOrDefault 'BEACON_INSTALL_ROOT' (Join-Path $env:ProgramFiles 'Beacon')
$beaconBin = Get-EnvOrDefault 'BEACON_BIN' (Join-Path $installRoot 'bin\beacon.exe')

if (-not (Test-Path -LiteralPath $beaconBin)) {
    Write-BeaconPackageLog "beacon.exe was not found at $beaconBin"
    throw "beacon.exe was not found at $beaconBin"
}

$harnesses = Get-BeaconSetting 'BEACON_ENDPOINT_HARNESSES' 'claude,codex'
$grpcPort = Get-BeaconSetting 'BEACON_OTLP_GRPC_PORT' '4317'
$httpPort = Get-BeaconSetting 'BEACON_OTLP_HTTP_PORT' '4318'
$collector = Get-BeaconSetting 'BEACON_COLLECTOR' ''
$service = Get-BeaconSetting 'BEACON_SERVICE' ''
$noStart = Get-BeaconSetting 'BEACON_NO_START' '0'
# shared or restricted; empty keeps whatever the endpoint already has, so an upgrade never changes
# the mode. Restricted makes the SYSTEM collector the only writer of the runtime log: hooks send their
# events to it over a local named pipe, and only SYSTEM, Administrators and the beacon-log group can
# read the log. The same variable the Linux and macOS install scripts read.
$logAccess = Get-BeaconSetting 'BEACON_LOG_ACCESS' ''

# A first install defaults to restricted, as on Linux and macOS: an MSI is installed by an
# administrator for the whole machine, often by Intune or SCCM across a fleet, and that is the install
# that should make the collector the only writer of the runtime log without a second step. Only a
# genuinely first install: an existing system config (an upgrade, or a reinstall over a kept config)
# or a runtime log left by `uninstall --keep-logs` passes no mode, so this never narrows or widens an
# existing endpoint. And only where the collector will be a Windows service, as on Linux, where it
# must be a systemd one: with BEACON_SERVICE=none the collector is a supervised process nothing
# restarts or starts at boot, and a restricted log there would leave every hook event spooled in its
# user's profile whenever that process is down, where a shared log keeps taking them directly. So a
# supervised install stays shared unless BEACON_LOG_ACCESS=restricted asks for it. BEACON_LOG_ACCESS=
# shared opts a new install out.
$systemBase = Join-Path $env:ProgramData 'Beacon\Endpoint'
$systemConfig = Get-EnvOrDefault 'BEACON_SYSTEM_CONFIG' (Join-Path $systemBase 'config.json')
$systemLog = Get-EnvOrDefault 'BEACON_SYSTEM_LOG' (Join-Path $systemBase 'logs\runtime.jsonl')
$firstInstall = -not (Test-Path -LiteralPath $systemConfig) -and -not (Test-Path -LiteralPath $systemLog)
# The --service values that resolve to the Service Control Manager (service.ParseKind): empty and
# auto auto-detect, which for a system install on Windows is the SCM.
$windowsService = @('', 'auto', 'windows-service', 'windows', 'scm') -contains $service.Trim().ToLowerInvariant()
if ([string]::IsNullOrWhiteSpace($logAccess) -and $firstInstall -and $windowsService) { $logAccess = 'restricted' }

# Accounts to add to the beacon-log local group of a restricted log, comma-separated (DOMAIN\name).
# Set -- in the environment, or as a policy value, even an empty one -- it is exactly the list: a
# fleet sets an empty BEACON_LOG_READERS policy value so that nobody is ever added implicitly. Unset,
# a first install that a person ran from the installer's full interface adds the person signed in at
# the console, so whoever installed Beacon on their own machine can use the dashboard, beacon traces
# and beacon share without a second step. A silent install (msiexec /qn, which Intune and SCCM run)
# adds nobody. Members are only ever added, never removed.
$logReaders = [Environment]::GetEnvironmentVariable('BEACON_LOG_READERS')
if ([string]::IsNullOrWhiteSpace($logReaders)) {
    if (Test-BeaconPolicy 'BEACON_LOG_READERS') {
        $logReaders = [string](Get-ItemProperty -LiteralPath $policyKey -Name 'BEACON_LOG_READERS').BEACON_LOG_READERS
    } else {
        $logReaders = ''
        $interactive = 0
        [void][int]::TryParse($UiLevel, [ref]$interactive)
        if ($firstInstall -and $logAccess -eq 'restricted' -and $interactive -ge 4) {
            $console = Get-EnvOrDefault 'BEACON_CONSOLE_USER' ''
            if ([string]::IsNullOrWhiteSpace($console)) {
                try { $console = [string](Get-CimInstance -ClassName Win32_ComputerSystem).UserName } catch { $console = '' }
            }
            if (-not [string]::IsNullOrWhiteSpace($console)) { $logReaders = $console.Trim() }
        }
    }
}
# enabled or disabled: whether any account may run `beacon endpoint dashboard`. Empty keeps what the
# endpoint already has, so an upgrade never changes it. The same variable the Linux and macOS install
# scripts read. An unattended first install (msiexec /qn, which Intune and SCCM run, or anything
# below the installer's full interface, by the same test as BEACON_LOG_READERS above) defaults to
# disabled: that install sets up the machine for the people who use it, who should not be able to
# open a view of its telemetry unless the administrator says so. A person who installs from the
# full interface keeps the dashboard. BEACON_DASHBOARD=enabled or disabled decides either way.
$dashboard = Get-BeaconSetting 'BEACON_DASHBOARD' ''
$uiLevelValue = 0
[void][int]::TryParse($UiLevel, [ref]$uiLevelValue)
if ([string]::IsNullOrWhiteSpace($dashboard) -and $firstInstall -and $uiLevelValue -lt 4) { $dashboard = 'disabled' }

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
if (-not [string]::IsNullOrWhiteSpace($logReaders)) { $installArgs += @('--log-readers', $logReaders.Trim()) }
if (-not [string]::IsNullOrWhiteSpace($dashboard)) { $installArgs += @('--dashboard', $dashboard) }

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
