# Builds the Beacon Endpoint Agent MSI from already-built binaries.
#
# Deliberately does not build Go code. The release job compiles the binaries once, for the targets it
# publishes, and this packages what it produced -- so an MSI can never contain a binary that differs
# from the one in the archive next to it.
#
# Usage:
#   .\build-msi.ps1 -Version 0.1.2 -BeaconExe ... -HooksExe ... -CollectorExe ... -OutFile ...
[CmdletBinding()]
param(
    [Parameter(Mandatory = $true)][string]$Version,
    [Parameter(Mandatory = $true)][string]$BeaconExe,
    [Parameter(Mandatory = $true)][string]$HooksExe,
    [Parameter(Mandatory = $true)][string]$CollectorExe,
    [string]$OutFile,
    # Pinned by the caller so a build is reproducible and a WiX release cannot change the output
    # underneath us.
    [string]$WixVersion = '5.0.2'
)

$ErrorActionPreference = 'Stop'
$scriptDir = Split-Path -Parent $MyInvocation.MyCommand.Path

# MSI ProductVersion is strictly numeric: major.minor.build, each field bounded. A tag like v0.1.2
# arrives with the v attached, and a snapshot version carries a suffix, neither of which MSI accepts
# -- so they are normalized here rather than failing deep inside WiX with a message about a field.
$normalized = $Version.TrimStart('v', 'V')
if ($normalized -match '^(\d+)\.(\d+)\.(\d+)') {
    $normalized = "$($Matches[1]).$($Matches[2]).$($Matches[3])"
} else {
    throw "version '$Version' is not major.minor.patch; MSI ProductVersion cannot represent it"
}

if ([string]::IsNullOrWhiteSpace($OutFile)) {
    $OutFile = Join-Path (Get-Location) "BeaconEndpointAgent-$normalized-x64.msi"
}

foreach ($input in @($BeaconExe, $HooksExe, $CollectorExe)) {
    if (-not (Test-Path -LiteralPath $input)) {
        throw "input binary not found: $input"
    }
}

# Staged into a layout the .wxs references by relative path, so the WiX source does not have to know
# where the caller keeps its build outputs.
$stage = Join-Path ([System.IO.Path]::GetTempPath()) ("beacon-msi-" + [Guid]::NewGuid().ToString('N'))
New-Item -ItemType Directory -Force -Path (Join-Path $stage 'bin'), (Join-Path $stage 'scripts') | Out-Null
try {
    Copy-Item -LiteralPath $BeaconExe -Destination (Join-Path $stage 'bin\beacon.exe')
    Copy-Item -LiteralPath $HooksExe -Destination (Join-Path $stage 'bin\beacon-hooks.exe')
    Copy-Item -LiteralPath $CollectorExe -Destination (Join-Path $stage 'bin\beacon-otelcol.exe')
    Copy-Item -LiteralPath (Join-Path $scriptDir 'install-endpoint.ps1') -Destination (Join-Path $stage 'scripts\install-endpoint.ps1')
    Copy-Item -LiteralPath (Join-Path $scriptDir 'uninstall-endpoint.ps1') -Destination (Join-Path $stage 'scripts\uninstall-endpoint.ps1')

    # The dotnet tool rather than a preinstalled WiX: it pins a version, it does not depend on what a
    # runner image happens to ship, and it works the same on a maintainer's machine.
    $wix = Get-Command wix -ErrorAction SilentlyContinue
    if (-not $wix) {
        Write-Output "installing WiX $WixVersion"
        & dotnet tool install --global wix --version $WixVersion
        if ($LASTEXITCODE -ne 0) { throw "installing WiX failed with exit code $LASTEXITCODE" }
        $env:PATH = "$env:PATH;$env:USERPROFILE\.dotnet\tools"
    }

    Write-Output "building $OutFile (version $normalized)"
    & wix build `
        -arch x64 `
        -d "Version=$normalized" `
        -d "StageDir=$stage" `
        -o $OutFile `
        (Join-Path $scriptDir 'beacon.wxs')
    if ($LASTEXITCODE -ne 0) { throw "wix build failed with exit code $LASTEXITCODE" }

    if (-not (Test-Path -LiteralPath $OutFile)) {
        throw "wix reported success but produced no file at $OutFile"
    }

    # The checksum ships beside the package for the same reason the macOS .pkg has one: it is the
    # only integrity check available on an unsigned artifact, and it stays mandatory once signing is
    # added rather than being replaced by it.
    $hash = (Get-FileHash -LiteralPath $OutFile -Algorithm SHA256).Hash.ToLower()
    "$hash  $(Split-Path -Leaf $OutFile)" | Set-Content -LiteralPath "$OutFile.sha256" -Encoding ascii
    Write-Output "sha256: $hash"
    Write-Output $OutFile
} finally {
    Remove-Item -LiteralPath $stage -Recurse -Force -ErrorAction SilentlyContinue
}
