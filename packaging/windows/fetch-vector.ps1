# Downloads the Vector build the Windows MSI ships, checks it, and stages vector.exe for build-msi.ps1.
#
#   .\fetch-vector.ps1 [-OutDir <dir>] [-Version 0.56.0]
#
# Writes <OutDir>\vector.exe and prints its path. OutDir defaults to cli\beacon\release-vector\windows_amd64.
#
# The archive is checked against packaging\vector\vector-<version>-SHA256SUMS, committed in this
# repository, never against a checksum file from the download host: vector.exe runs as a SYSTEM
# service, so a compromised download host must not be able to supply both the binary and its hash.
# This is the Windows half of packaging/linux/fetch-vector.sh, and the version is pinned to match it.
#
# Extracted with Expand-Archive on Windows. Upstream builds the zip with Compress-Archive, so its
# entry names use backslashes, which zip tools on macOS and Linux turn into file names rather than
# directories.
[CmdletBinding()]
param(
    [string]$OutDir,
    [string]$Version = '0.56.0'
)

$ErrorActionPreference = 'Stop'
$ProgressPreference = 'SilentlyContinue'
$root = (Resolve-Path (Join-Path (Split-Path -Parent $MyInvocation.MyCommand.Path) '..\..')).Path
if ([string]::IsNullOrWhiteSpace($OutDir)) {
    $OutDir = Join-Path $root 'cli\beacon\release-vector\windows_amd64'
}

$archive = "vector-$Version-x86_64-pc-windows-msvc.zip"
$pinned = Join-Path $root "packaging\vector\vector-$Version-SHA256SUMS"
if (-not (Test-Path -LiteralPath $pinned)) {
    throw "no pinned checksums at $pinned; commit them for Vector $Version first"
}
$line = Get-Content -LiteralPath $pinned | Where-Object { ($_ -split '\s+')[1] -eq $archive } | Select-Object -First 1
if (-not $line) { throw "no pinned checksum for $archive in $pinned" }
$expected = ($line -split '\s+')[0].ToLower()

$work = Join-Path ([System.IO.Path]::GetTempPath()) ("beacon-vector-" + [Guid]::NewGuid().ToString('N'))
New-Item -ItemType Directory -Force -Path $work | Out-Null
try {
    $zip = Join-Path $work $archive
    $url = "https://packages.timber.io/vector/$Version/$archive"
    for ($attempt = 1; ; $attempt++) {
        try {
            Invoke-WebRequest -Uri $url -OutFile $zip -UseBasicParsing
            break
        } catch {
            if ($attempt -ge 3) { throw "downloading ${url}: $($_.Exception.Message)" }
            Start-Sleep -Seconds (5 * $attempt)
        }
    }
    $actual = (Get-FileHash -LiteralPath $zip -Algorithm SHA256).Hash.ToLower()
    if ($actual -ne $expected) {
        throw "$archive has SHA256 $actual; the pinned checksum is $expected"
    }

    $extracted = Join-Path $work 'extracted'
    Expand-Archive -LiteralPath $zip -DestinationPath $extracted
    $exe = Join-Path $extracted 'bin\vector.exe'
    if (-not (Test-Path -LiteralPath $exe)) { throw "no bin\vector.exe in $archive" }

    $reported = (& $exe --version) -join ' '
    if ($LASTEXITCODE -ne 0 -or $reported -notmatch "vector $([regex]::Escape($Version))\b") {
        throw "$exe reported '$reported', not Vector $Version"
    }

    New-Item -ItemType Directory -Force -Path $OutDir | Out-Null
    $dest = Join-Path $OutDir 'vector.exe'
    Copy-Item -LiteralPath $exe -Destination $dest -Force
    Write-Output $dest
} finally {
    Remove-Item -LiteralPath $work -Recurse -Force -ErrorAction SilentlyContinue
}
