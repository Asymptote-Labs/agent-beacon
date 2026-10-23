# Fails unless every file carries a valid, timestamped Authenticode signature from the expected
# publisher. Windows only: Get-AuthenticodeSignature does not exist in PowerShell on Linux or macOS.
#
# Usage:
#   .\assert-authenticode.ps1 -Path beacon.exe, BeaconEndpointAgent-1.2.3-x64.msi
[CmdletBinding()]
param(
    [Parameter(Mandatory = $true)][string[]]$Path,
    [string]$Publisher = 'Asymptote Labs, Inc.'
)

$ErrorActionPreference = 'Stop'
$failures = 0
foreach ($file in $Path) {
    $sig = Get-AuthenticodeSignature -LiteralPath $file
    $signer = if ($sig.SignerCertificate) {
        $sig.SignerCertificate.GetNameInfo([System.Security.Cryptography.X509Certificates.X509NameType]::SimpleName, $false)
    }
    if ($sig.Status -ne 'Valid') {
        Write-Output "FAIL: $file signature is $($sig.Status): $($sig.StatusMessage)"
    } elseif ($signer -ne $Publisher) {
        Write-Output "FAIL: $file is signed by '$signer', want '$Publisher'"
    } elseif (-not $sig.TimeStamperCertificate) {
        Write-Output "FAIL: $file has no timestamp, so its signature expires with the certificate"
    } else {
        Write-Output "ok: $file is signed by $signer"
        continue
    }
    $failures++
}
if ($failures -gt 0) { exit 1 }
