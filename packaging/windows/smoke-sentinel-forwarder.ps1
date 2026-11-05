# End-to-end check of the managed Sentinel forwarder on an installed Windows endpoint, against a mock
# of Microsoft Entra ID and the Azure Monitor Logs Ingestion API (sentinel-mock.exe). It replays what
# a fleet does: install the MSI, set up the forwarder as SYSTEM with a certificate, upgrade, switch log
# access, uninstall. Run elevated, which the CI runner is; each phase runs at its point in the job.
#
#   -Phase setup          after the first install: mock up, forwarder installed, first rows arrive,
#                         and nobody but SYSTEM and Administrators can read the credential
#   -Phase after-upgrade  after the MSI upgrade: the service is back, on the new package's Vector
#   -Phase verify         after the log access switches: every event in the runtime log reached the
#                         mock, and none twice
#   -Phase after-uninstall after a non-purge uninstall: the service is gone, its state kept
#   -Phase cleanup        always: stop the mock, remove its CA and the test certificate
[CmdletBinding()]
param(
    [Parameter(Mandatory = $true)]
    [ValidateSet('setup', 'after-upgrade', 'verify', 'after-uninstall', 'cleanup')]
    [string]$Phase,
    [string]$MockExe
)

$ErrorActionPreference = 'Stop'

$beacon = Join-Path $env:ProgramFiles 'Beacon\bin\beacon.exe'
$service = 'BeaconSentinelForwarder'
$forwarderDir = Join-Path $env:ProgramData 'Beacon\Endpoint\forwarders\sentinel'
$logDir = Join-Path $env:ProgramData 'Beacon\Endpoint\logs'
$work = Join-Path $env:RUNNER_TEMP 'sentinel-smoke'
$rowsFile = Join-Path $work 'rows.jsonl'
$urlFile = Join-Path $work 'url'
$caFile = Join-Path $work 'mock-ca.pem'
$pfx = Join-Path $work 'app.pfx'
$tenant = '11111111-2222-3333-4444-555555555555'
$client = '66666666-7777-8888-9999-000000000000'
$dcr = 'dcr-0123456789abcdef0123456789abcdef'
$pfxPassword = 'smoke-pfx-password'

function Invoke-Beacon {
    param([string[]]$Arguments)
    $out = & $beacon @Arguments 2>&1 | Out-String
    if ($LASTEXITCODE -ne 0) {
        Write-Output $out
        throw "beacon $($Arguments -join ' ') exited $LASTEXITCODE"
    }
    return $out
}

function Get-Rows {
    if (-not (Test-Path -LiteralPath $rowsFile)) { return @() }
    $rows = @()
    # The mock appends while this reads; open allowing that.
    $stream = [System.IO.File]::Open($rowsFile, 'Open', 'Read', 'ReadWrite')
    try {
        $reader = New-Object System.IO.StreamReader($stream)
        while ($null -ne ($line = $reader.ReadLine())) {
            if ($line.Trim()) { $rows += ($line | ConvertFrom-Json) }
        }
    } finally {
        $stream.Dispose()
    }
    return $rows
}

function Get-RowIds {
    $ids = @{}
    foreach ($row in Get-Rows) {
        $id = ($row.RawData | ConvertFrom-Json).event.id
        $ids[$id] = 1 + [int]$ids[$id]
    }
    return $ids
}

# Every event id in the runtime log and the archive a rotation made, read as this elevated account.
function Get-LogIds {
    $ids = @()
    foreach ($name in @('runtime.jsonl.1', 'runtime.jsonl')) {
        $path = Join-Path $logDir $name
        if (-not (Test-Path -LiteralPath $path)) { continue }
        $stream = [System.IO.File]::Open($path, 'Open', 'Read', 'ReadWrite, Delete')
        try {
            $reader = New-Object System.IO.StreamReader($stream)
            while ($null -ne ($line = $reader.ReadLine())) {
                if ($line.Trim()) { $ids += ($line | ConvertFrom-Json).event.id }
            }
        } finally {
            $stream.Dispose()
        }
    }
    return $ids
}

function Wait-Validation {
    param([int]$Count, [int]$Seconds = 120)
    $deadline = (Get-Date).AddSeconds($Seconds)
    while ($true) {
        $found = @(Get-Rows | Where-Object { $_.RawData -like '*Beacon endpoint Sentinel validation event*' }).Count
        if ($found -ge $Count) { return }
        if ((Get-Date) -gt $deadline) {
            Show-Forwarder
            throw "$found of $Count Sentinel validation events reached the mock in ${Seconds}s"
        }
        Start-Sleep -Seconds 2
    }
}

function Assert-Running {
    $state = (& sc.exe query $service 2>&1 | Out-String)
    $global:LASTEXITCODE = 0
    if ($state -notmatch 'RUNNING') {
        Write-Output $state
        Show-Forwarder
        throw "the $service service is not running"
    }
}

function Show-Forwarder {
    Write-Output "--- $service ---"
    & sc.exe queryex $service 2>&1 | Write-Output
    & sc.exe qc $service 2>&1 | Write-Output
    & $beacon endpoint sentinel forwarder status --system 2>&1 | Write-Output
    Get-ChildItem (Join-Path $forwarderDir 'logs') -Filter 'vector-*.log' -ErrorAction SilentlyContinue |
        ForEach-Object { Write-Output "--- $($_.Name) (tail) ---"; Get-Content -LiteralPath $_.FullName -Tail 30 | Write-Output }
    $global:LASTEXITCODE = 0
}

# Only SYSTEM and Administrators may be granted anything on the credential and its directory.
function Assert-Private {
    param([string]$Path)
    $allowed = @('NT AUTHORITY\SYSTEM', 'BUILTIN\Administrators')
    $acl = Get-Acl -LiteralPath $Path
    foreach ($rule in $acl.Access) {
        if ($rule.AccessControlType -eq 'Allow' -and $allowed -notcontains $rule.IdentityReference.Value) {
            throw "$Path grants $($rule.FileSystemRights) to $($rule.IdentityReference.Value)"
        }
    }
    Write-Output "ok: only SYSTEM and Administrators can open $Path"
}

switch ($Phase) {
    'setup' {
        New-Item -ItemType Directory -Force -Path $work | Out-Null
        if (-not $MockExe -or -not (Test-Path -LiteralPath $MockExe)) { throw "-MockExe must name the built sentinel-mock.exe" }

        # A certificate credential, the one the docs lead with, made with `openssl pkcs12 -export`.
        $openssl = (Get-Command openssl -ErrorAction SilentlyContinue).Source
        if (-not $openssl) { $openssl = Join-Path $env:ProgramFiles 'Git\usr\bin\openssl.exe' }
        $key = Join-Path $work 'app-key.pem'
        $certPem = Join-Path $work 'app-cert.pem'
        & $openssl req -x509 -newkey rsa:2048 -nodes -keyout $key -out $certPem -days 2 -subj '/CN=beacon-sentinel-smoke' 2>&1 | Out-Null
        & $openssl pkcs12 -export -inkey $key -in $certPem -out $pfx -passout "pass:$pfxPassword" 2>&1 | Out-Null
        if ($LASTEXITCODE -ne 0 -or -not (Test-Path -LiteralPath $pfx)) { throw "openssl could not make the test PFX" }
        Remove-Item -LiteralPath $key

        # The PFX encodings the docs say work: Windows' Export-PfxCertificate with either algorithm,
        # and OpenSSL's default. The bundled Vector has to read every one.
        $cert = New-SelfSignedCertificate -Subject 'CN=beacon-sentinel-smoke-export' -CertStoreLocation Cert:\LocalMachine\My `
            -KeyAlgorithm RSA -KeyLength 2048 -KeyExportPolicy Exportable -NotAfter (Get-Date).AddDays(2)
        $cert.Thumbprint | Set-Content -LiteralPath (Join-Path $work 'cert-thumbprint')
        $secure = ConvertTo-SecureString $pfxPassword -AsPlainText -Force
        $variants = [ordered]@{ 'openssl pkcs12 -export' = $pfx }
        foreach ($algorithm in @('AES256_SHA256', 'TripleDES_SHA1')) {
            $path = Join-Path $work "export-$algorithm.pfx"
            Export-PfxCertificate -Cert $cert -FilePath $path -Password $secure -CryptoAlgorithmOption $algorithm | Out-Null
            $variants["Export-PfxCertificate -CryptoAlgorithmOption $algorithm"] = $path
        }
        $vectorExe = Join-Path $env:ProgramFiles 'Beacon\bin\vector.exe'
        foreach ($name in $variants.Keys) {
            $probe = Join-Path $work 'pfx-probe.toml'
            @"
data_dir = '$work'
[sources.probe_source]
type = 'demo_logs'
format = 'json'
count = 1
[sinks.probe_sink]
type = 'azure_logs_ingestion'
inputs = ['probe_source']
endpoint = 'https://127.0.0.1:9'
dcr_immutable_id = '$dcr'
stream_name = 'Custom-BeaconRuntimeRaw'
[sinks.probe_sink.auth]
azure_credential_kind = 'client_certificate_credential'
azure_tenant_id = '$tenant'
azure_client_id = '$client'
certificate_path = '$($variants[$name])'
certificate_password = '$pfxPassword'
"@ | Set-Content -LiteralPath $probe -Encoding ascii
            $out = & $vectorExe validate --skip-healthchecks $probe 2>&1 | Out-String
            if ($LASTEXITCODE -ne 0) {
                Write-Output $out
                throw "the bundled Vector cannot read a PFX made with $name"
            }
            Write-Output "ok: the bundled Vector reads a PFX made with $name"
        }

        # The mock outlives this step: it keeps answering through the upgrade and the log access
        # switches, until the cleanup phase stops it.
        $mock = Start-Process -FilePath $MockExe -PassThru -WindowStyle Hidden `
            -RedirectStandardOutput (Join-Path $work 'mock.out') -RedirectStandardError (Join-Path $work 'mock.err') `
            -ArgumentList @('--ca-out', $caFile, '--rows-out', $rowsFile, '--url-out', $urlFile,
                '--tenant', $tenant, '--client-id', $client, '--dcr', $dcr,
                # A role assignment that has not propagated yet: the forwarder must retry, not drop.
                '--ingest-statuses', '403,403')
        $mock.Id | Set-Content -LiteralPath (Join-Path $work 'mock.pid')
        $deadline = (Get-Date).AddSeconds(30)
        while (-not (Test-Path -LiteralPath $urlFile)) {
            if ((Get-Date) -gt $deadline) { Get-Content (Join-Path $work 'mock.err') -ErrorAction SilentlyContinue; throw "the mock never started" }
            Start-Sleep -Milliseconds 200
        }
        $url = (Get-Content -LiteralPath $urlFile -Raw).Trim()
        # Vector's token client trusts only the Windows store, as SYSTEM.
        & certutil.exe -addstore -f Root $caFile | Out-Null
        if ($LASTEXITCODE -ne 0) { throw "certutil -addstore Root failed" }
        Write-Output "mock at $url"

        $env:AZURE_CLIENT_CERTIFICATE_PASSWORD = $pfxPassword
        Invoke-Beacon @('endpoint', 'sentinel', 'forwarder', 'install', '--system',
            '--endpoint', $url, '--authority-host', $url, '--dcr-immutable-id', $dcr,
            '--tenant-id', $tenant, '--client-id', $client, '--client-certificate', $pfx) | Write-Output
        Remove-Item Env:\AZURE_CLIENT_CERTIFICATE_PASSWORD
        Assert-Running

        $environment = (Get-ItemProperty "HKLM:\SYSTEM\CurrentControlSet\Services\$service").Environment
        if ($environment -notcontains "AZURE_AUTHORITY_HOST=$url") { throw "the service environment is '$environment'" }

        # The deployment's copy of the certificate can go; the forwarder keeps its own.
        Remove-Item -LiteralPath $pfx
        foreach ($path in @($forwarderDir, (Join-Path $forwarderDir 'secrets.json'), (Join-Path $forwarderDir 'client.pfx'))) {
            Assert-Private $path
        }
        $settings = Get-Content -LiteralPath (Join-Path $forwarderDir 'forwarder.json') -Raw
        if ($settings -match [regex]::Escape($pfxPassword)) { throw "forwarder.json holds the certificate password" }

        Invoke-Beacon @('endpoint', 'sentinel', 'validate', '--system') | Out-Null
        Wait-Validation -Count 1
        $status = Invoke-Beacon @('endpoint', 'status', '--system', '--json') | ConvertFrom-Json
        if (-not $status.sentinel_forwarder.service.running) { throw "endpoint status does not report the forwarder running" }
        Write-Output "ok: the forwarder authenticated with a certificate, retried 403s, and delivered"
    }
    'after-upgrade' {
        # The old package's uninstall removed the service so vector.exe could be replaced; the new
        # package's install put it back from the state it kept.
        Assert-Running
        Invoke-Beacon @('endpoint', 'sentinel', 'validate', '--system') | Out-Null
        Wait-Validation -Count 2
        Write-Output "ok: the forwarder came back after the upgrade and kept delivering"
    }
    'verify' {
        Assert-Running
        Invoke-Beacon @('endpoint', 'sentinel', 'validate', '--system') | Out-Null
        Wait-Validation -Count 3
        $deadline = (Get-Date).AddSeconds(120)
        while ($true) {
            $logIds = Get-LogIds
            $rowIds = Get-RowIds
            $missing = @($logIds | Where-Object { -not $rowIds.ContainsKey($_) })
            if ($missing.Count -eq 0) { break }
            if ((Get-Date) -gt $deadline) {
                Show-Forwarder
                throw "$($missing.Count) of $($logIds.Count) runtime log events never reached the mock, first: $($missing[0])"
            }
            Start-Sleep -Seconds 3
        }
        $twice = @($rowIds.Keys | Where-Object { $rowIds[$_] -gt 1 })
        if ($twice.Count -gt 0) { throw "$($twice.Count) events reached the mock more than once, first: $($twice[0])" }
        Write-Output "ok: all $($logIds.Count) runtime log events reached the mock exactly once, across an upgrade and two log access switches"
    }
    'after-uninstall' {
        $query = (& sc.exe query $service 2>&1 | Out-String)
        $global:LASTEXITCODE = 0
        if ($query -notmatch '1060') { Write-Output $query; throw "the $service service still exists after uninstall" }
        if (-not (Test-Path -LiteralPath (Join-Path $forwarderDir 'forwarder.json'))) {
            throw "a non-purge uninstall removed the forwarder's settings"
        }
        Write-Output "ok: uninstall removed the forwarder service and kept its settings for a reinstall"
    }
    'cleanup' {
        $ErrorActionPreference = 'Continue'
        $pidFile = Join-Path $work 'mock.pid'
        if (Test-Path -LiteralPath $pidFile) {
            Stop-Process -Id ([int](Get-Content -LiteralPath $pidFile)) -Force -ErrorAction SilentlyContinue
        }
        if (Test-Path -LiteralPath $caFile) {
            $ca = New-Object System.Security.Cryptography.X509Certificates.X509Certificate2($caFile)
            Get-ChildItem Cert:\LocalMachine\Root | Where-Object Thumbprint -eq $ca.Thumbprint | Remove-Item
        }
        $thumbFile = Join-Path $work 'cert-thumbprint'
        if (Test-Path -LiteralPath $thumbFile) {
            $thumb = (Get-Content -LiteralPath $thumbFile).Trim()
            Get-ChildItem Cert:\LocalMachine\My | Where-Object Thumbprint -eq $thumb | Remove-Item
        }
        $global:LASTEXITCODE = 0
    }
}
