param(
    [string]$SbxPath = 'sbx',
    [Parameter(Mandatory=$true)][string]$EvidenceRoot,
    [Parameter(Mandatory=$true)][string]$SandboxA,
    [Parameter(Mandatory=$true)][string]$SandboxB,
    [Parameter(Mandatory=$true)][ValidateRange(1,65535)][int]$GatewayPortA,
    [Parameter(Mandatory=$true)][ValidateRange(1,65535)][int]$GatewayPortB,
    [Parameter(Mandatory=$true)][string]$HostCanaryGuestPath
)

$ErrorActionPreference = 'Stop'
$evidence = [System.IO.Path]::GetFullPath($EvidenceRoot)
[System.IO.Directory]::CreateDirectory($evidence) | Out-Null
$results = @()
function Invoke-Probe {
    param([string]$Id, [string[]]$SbxArgs)
    $previousPreference = $ErrorActionPreference
    $ErrorActionPreference = 'Continue'
    try {
        $raw = & $SbxPath @SbxArgs 2>&1
        $code = $LASTEXITCODE
    } finally { $ErrorActionPreference = $previousPreference }
    $text = ($raw | ForEach-Object { [string]$_ }) -join [Environment]::NewLine
    Write-Output ($Id + ' exit=' + $code)
    Write-Output $text
    $script:results += [pscustomobject]@{id=$Id; at=(Get-Date).ToString('o'); exitCode=$code; args=$SbxArgs; output=$text}
}

$env:SBX_NO_TELEMETRY = '1'
$a = $SandboxA
$b = $SandboxB
$gatewayA = 'http://host.docker.internal:' + $GatewayPortA
$gatewayB = 'http://host.docker.internal:' + $GatewayPortB
try {
    Invoke-Probe 'a-own-health' @('exec',$a,'/tmp/guest-probe','request','-url',($gatewayA + '/health'))
    Invoke-Probe 'a-own-model' @('exec',$a,'/tmp/guest-probe','request','-url',($gatewayA + '/invoke'),'-method','POST','-token','PULSAR_SYNTHETIC_a')
    Invoke-Probe 'a-wrong-token' @('exec',$a,'/tmp/guest-probe','request','-url',($gatewayA + '/invoke'),'-method','POST','-token','PULSAR_SYNTHETIC_b')
    Invoke-Probe 'b-own-model' @('exec',$b,'/tmp/guest-probe','request','-url',($gatewayB + '/invoke'),'-method','POST','-token','PULSAR_SYNTHETIC_b')
    Invoke-Probe 'a-borrowed-b-token' @('exec',$a,'/tmp/guest-probe','request','-url',($gatewayB + '/invoke'),'-method','POST','-token','PULSAR_SYNTHETIC_b')
    Invoke-Probe 'b-borrowed-a-token' @('exec',$b,'/tmp/guest-probe','request','-url',($gatewayA + '/invoke'),'-method','POST','-token','PULSAR_SYNTHETIC_a')
    Invoke-Probe 'a-unapproved-domain' @('exec',$a,'/tmp/guest-probe','request','-url','https://example.com/')
    Invoke-Probe 'a-direct-domain' @('exec',$a,'/tmp/guest-probe','request','-url','https://example.com/','-proxy','none')
    Invoke-Probe 'a-direct-ip' @('exec',$a,'/tmp/guest-probe','request','-url','https://1.1.1.1/','-proxy','none')
    Invoke-Probe 'a-direct-ipv6' @('exec',$a,'/tmp/guest-probe','request','-url','https://[2606:4700:4700::1111]/','-proxy','none')
    Invoke-Probe 'a-unapproved-host-port' @('exec',$a,'/tmp/guest-probe','request','-url','http://host.docker.internal:3425/')
    Invoke-Probe 'a-write-private-file' @('exec',$a,'/tmp/guest-probe','write','/tmp/pulsar-probe/only-a.txt','PULSAR_SYNTHETIC_A_ONLY')
    Invoke-Probe 'b-a-file-absent' @('exec',$b,'/tmp/guest-probe','absent','/tmp/pulsar-probe/only-a.txt')
    Invoke-Probe 'a-host-paths-absent' @('exec',$a,'/tmp/guest-probe','absent',$HostCanaryGuestPath,'/run/sandbox/source')
    Invoke-Probe 'b-host-paths-absent' @('exec',$b,'/tmp/guest-probe','absent',$HostCanaryGuestPath,'/run/sandbox/source')
    Invoke-Probe 'a-network-log' @('policy','log',$a,'--json','--limit','40')
    Invoke-Probe 'b-network-log' @('policy','log',$b,'--json','--limit','20')
} finally {
    $path = Join-Path $evidence 'sandbox-probe-results.json'
    [System.IO.File]::WriteAllText($path, ($script:results | ConvertTo-Json -Depth 6), (New-Object System.Text.UTF8Encoding($false)))
    Write-Output ('Results: ' + $path)
}
