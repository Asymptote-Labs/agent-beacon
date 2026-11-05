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

. (Join-Path $PSScriptRoot 'package-common.ps1')
Open-BeaconPackageLog 'uninstall'

# Long enough for `endpoint uninstall` to stop the service (it waits up to 20 seconds for a graceful
# stop) and remove what it installed. The bound exists because this action also runs at the start of
# every upgrade, where a call that never returns holds the upgrade open indefinitely.
$uninstallTimeoutSeconds = 180

# Makes sure the collector is no longer running, for when `endpoint uninstall` could not finish.
#
# A stopped collector is the one thing the MSI cannot proceed without: it cannot replace or remove
# beacon-otelcol.exe while the service holds it open, and an upgrade that carried on regardless would
# defer the replacement to a reboot or keep running the old collector. The registration goes too, so
# a removal leaves no service pointing at a deleted binary; an upgrade's install action registers it
# again.
function Stop-BeaconService {
    param([string]$Name, [string]$Executable)
    # Best effort throughout: sc.exe reports a service that is already gone on stderr, which the
    # script's 'Stop' preference would otherwise turn into a failed uninstall.
    $ErrorActionPreference = 'Continue'
    $service = Get-Service -Name $Name -ErrorAction SilentlyContinue
    if ($null -eq $service) { return }
    if ($service.Status -ne 'Stopped') {
        Write-BeaconPackageLog "stopping the $Name service directly"
        & sc.exe stop $Name 2>&1 | ForEach-Object { Write-BeaconPackageLog "  $_" }
        try {
            $service.WaitForStatus('Stopped', [TimeSpan]::FromSeconds(30))
        } catch {
            Write-BeaconPackageLog "the $Name service did not stop within 30s"
        }
    }
    $processName = [System.IO.Path]::GetFileNameWithoutExtension($Executable)
    Get-Process -Name $processName -ErrorAction SilentlyContinue |
        Where-Object { $_.Path -eq $Executable } |
        ForEach-Object {
            Write-BeaconPackageLog "terminating $processName process $($_.Id)"
            Stop-Process -Id $_.Id -Force -ErrorAction SilentlyContinue
        }
    & sc.exe delete $Name 2>&1 | ForEach-Object { Write-BeaconPackageLog "  $_" }
    $global:LASTEXITCODE = 0
}

# The services that hold a file the package is about to remove or replace: the collector, and the
# Sentinel forwarder's Vector when one is set up.
function Stop-BeaconServices {
    param([string]$InstallRoot)
    Stop-BeaconService -Name 'BeaconCollector' -Executable (Join-Path $InstallRoot 'bin\beacon-otelcol.exe')
    Stop-BeaconService -Name 'BeaconSentinelForwarder' -Executable (Join-Path $InstallRoot 'bin\vector.exe')
}

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
    Write-BeaconPackageLog "beacon: $beaconBin is not present; nothing to uninstall"
    Close-BeaconPackageLog
    exit 0
}

$configDir = Join-Path $env:ProgramData 'Beacon\Endpoint'
$retained = @('config.json', 'otelcol.yaml', 'otlp-forward-headers.json')

# A file `beacon endpoint install --system` wrote with a protected DACL (SYSTEM and Administrators
# only, because it holds a credential: a HEC token, the OTLP forward's headers) has to come back with
# that DACL. Copy-Item would give the copy what the destination directory passes on, which every
# user can read. So such a file is copied into a new file created with the original descriptor --
# set by the creation itself, so there is no moment anyone else could open it -- and renamed into
# place. Other files keep the plain copy.
$protected = @{}
function Copy-Retained([string]$source, [string]$destination, [string]$sddl) {
    if (-not $sddl) {
        Copy-Item -LiteralPath $source -Destination $destination -Force
        return
    }
    $security = New-Object System.Security.AccessControl.FileSecurity
    $security.SetSecurityDescriptorSddlForm($sddl)
    $partial = "$destination.restoring"
    Remove-Item -LiteralPath $partial -Force -ErrorAction SilentlyContinue
    $bytes = [System.IO.File]::ReadAllBytes($source)
    if ($PSVersionTable.PSEdition -eq 'Core') {
        # PowerShell 7 (.NET) has no File.Create overload that takes a FileSecurity; the extension
        # method is its equivalent, still setting the descriptor as the file is created.
        Add-Type -AssemblyName System.IO.FileSystem.AccessControl -ErrorAction SilentlyContinue
        $stream = [System.IO.FileSystemAclExtensions]::Create(
            [System.IO.FileInfo]::new($partial), [System.IO.FileMode]::CreateNew,
            [System.Security.AccessControl.FileSystemRights]::Write, [System.IO.FileShare]::None,
            4096, [System.IO.FileOptions]::None, $security)
    } else {
        # Windows PowerShell 5.1 (.NET Framework), which the MSI runs.
        $stream = [System.IO.File]::Create($partial, 4096, [System.IO.FileOptions]::None, $security)
    }
    try {
        $stream.Write($bytes, 0, $bytes.Length)
    } finally {
        $stream.Close()
    }
    Move-Item -LiteralPath $partial -Destination $destination -Force
}

$stash = $null
if ($purge -ne '1') {
    $stash = Join-Path ([System.IO.Path]::GetTempPath()) ("beacon-uninstall-" + [Guid]::NewGuid().ToString('N'))
    New-Item -ItemType Directory -Force -Path $stash | Out-Null
    foreach ($name in $retained) {
        $source = Join-Path $configDir $name
        if (Test-Path -LiteralPath $source) {
            $acl = Get-Acl -LiteralPath $source
            if ($acl.AreAccessRulesProtected) {
                $protected[$name] = $acl.Sddl
            }
            Copy-Retained $source (Join-Path $stash $name) $protected[$name]
        }
    }
    Write-BeaconPackageLog "beacon: removing the endpoint, keeping configuration and logs (set BEACON_PURGE=1 to remove them)"
} else {
    Write-BeaconPackageLog "beacon: removing the endpoint, configuration and logs"
}

$uninstallArgs = @('endpoint', 'uninstall', '--system')
if ($purge -ne '1') {
    # --keep-logs does what its name says. --keep-config is passed for the harness settings it does
    # govern; the stash above is what actually preserves Beacon's own configuration.
    $uninstallArgs += @('--keep-logs', '--keep-config')
}

$code = Invoke-BeaconBounded -Beacon $beaconBin -Arguments $uninstallArgs -TimeoutSeconds $uninstallTimeoutSeconds
if ($code -eq $script:BeaconTimeoutExitCode) {
    # Stopped, not waited on. A removal that hangs is worse for the person running it than one that
    # finishes with the collector forced down, and the package log records what happened.
    Stop-BeaconServices -InstallRoot $installRoot
} elseif ($code -ne 0) {
    # Reported, not thrown. The service and its registration are what matter here, and `endpoint
    # uninstall` is idempotent -- so a non-zero exit is worth surfacing but not worth wedging an MSI
    # removal over. A user who cannot uninstall the package has a worse problem than a leftover file.
    Write-BeaconPackageLog "beacon: endpoint uninstall exited $code; check 'beacon endpoint status --system'"
}

if ($null -ne $stash) {
    try {
        $restored = 0
        foreach ($name in $retained) {
            $saved = Join-Path $stash $name
            if (Test-Path -LiteralPath $saved) {
                New-Item -ItemType Directory -Force -Path $configDir | Out-Null
                Copy-Retained $saved (Join-Path $configDir $name) $protected[$name]
                $restored++
            }
        }
        if ($restored -gt 0) {
            Write-BeaconPackageLog "beacon: restored $restored configuration file(s) to $configDir"
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

Write-BeaconPackageLog "beacon: endpoint removed"
Close-BeaconPackageLog
