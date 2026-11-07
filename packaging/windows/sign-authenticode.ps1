# Authenticode-signs Windows binaries and installers with Azure Trusted Signing.
#
# jsign rather than signtool, so the same script signs the .exe files on the Linux goreleaser job and
# the MSI on the Windows packaging job. AZURE_TRUSTED_SIGNING_TOKEN is an access token for
# https://codesigning.azure.net, which .github/actions/trusted-signing-login exports.
#
# Does nothing unless AZURE_TRUSTED_SIGNING_ACCOUNT is set, so a release without signing configured
# still ships, unsigned, as before.
#
# Usage:
#   ./sign-authenticode.ps1 -Path beacon.exe, beacon-hooks.exe
[CmdletBinding()]
param(
    [Parameter(Mandatory = $true)][string[]]$Path
)

$ErrorActionPreference = 'Stop'

$account = $env:AZURE_TRUSTED_SIGNING_ACCOUNT
if ([string]::IsNullOrWhiteSpace($account)) {
    Write-Output 'Authenticode signing is not configured (AZURE_TRUSTED_SIGNING_ACCOUNT is unset); leaving files unsigned'
    exit 0
}
foreach ($name in @('AZURE_TRUSTED_SIGNING_ENDPOINT', 'AZURE_TRUSTED_SIGNING_PROFILE', 'AZURE_TRUSTED_SIGNING_TOKEN')) {
    if ([string]::IsNullOrWhiteSpace([Environment]::GetEnvironmentVariable($name))) {
        throw "$name must be set when AZURE_TRUSTED_SIGNING_ACCOUNT is"
    }
}
foreach ($file in $Path) {
    if (-not (Test-Path -LiteralPath $file)) { throw "file to sign not found: $file" }
}

$jsignVersion = '7.5'
$jsignSha256 = '602a51c3545a6dc4fb99bd2ea7152b26d1345916d0c93ddfbd5936cb735af91c'
$jar = Join-Path ([System.IO.Path]::GetTempPath()) "jsign-$jsignVersion.jar"
if (-not (Test-Path -LiteralPath $jar) -or (Get-FileHash -LiteralPath $jar -Algorithm SHA256).Hash -ne $jsignSha256) {
    Invoke-WebRequest -Uri "https://repo1.maven.org/maven2/net/jsign/jsign/$jsignVersion/jsign-$jsignVersion.jar" -OutFile $jar
    $actual = (Get-FileHash -LiteralPath $jar -Algorithm SHA256).Hash
    if ($actual -ne $jsignSha256) {
        Remove-Item -LiteralPath $jar -Force
        throw "jsign-$jsignVersion.jar has sha256 $actual, want $jsignSha256"
    }
}

$endpoint = $env:AZURE_TRUSTED_SIGNING_ENDPOINT -replace '^https://', '' -replace '/+$', ''
# The token reaches jsign through the environment, not the command line, where every process could read it.
& java -jar $jar `
    --storetype TRUSTEDSIGNING `
    --keystore $endpoint `
    --storepass env:AZURE_TRUSTED_SIGNING_TOKEN `
    --alias "$account/$($env:AZURE_TRUSTED_SIGNING_PROFILE)" `
    --alg SHA-256 `
    --tsaurl http://timestamp.acs.microsoft.com `
    --tsmode RFC3161 `
    --tsretries 3 `
    --name 'Beacon Endpoint Agent' `
    --url https://docs.asymptotelabs.ai `
    @Path
if ($LASTEXITCODE -ne 0) { throw "jsign failed with exit code $LASTEXITCODE" }
Write-Output "signed: $($Path -join ', ')"
