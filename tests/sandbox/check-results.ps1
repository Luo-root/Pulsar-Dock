param(
    [Parameter(Mandatory=$true)][string]$EvidenceRoot,
    [Parameter(Mandatory=$true)][string]$SandboxA,
    [Parameter(Mandatory=$true)][string]$SandboxB
)

$ErrorActionPreference = 'Stop'
$root = [System.IO.Path]::GetFullPath($EvidenceRoot)
$checks = @()
function Read-Record([string]$name) {
    $raw = [System.IO.File]::ReadAllText((Join-Path $root $name), [System.Text.Encoding]::UTF8)
    return ($raw | ConvertFrom-Json)
}
function Parse-Probe([string]$text) {
    $object = $text.IndexOf('{')
    $array = $text.IndexOf('[')
    $start = $object
    if ($array -ge 0 -and ($start -lt 0 -or $array -lt $start)) { $start = $array }
    if ($start -lt 0) { throw 'Probe did not contain JSON' }
    return ($text.Substring($start) | ConvertFrom-Json)
}
function Check([string]$id, [bool]$passed) {
    $script:checks += [pscustomobject]@{id=$id; passed=$passed}
    Write-Output ($id + ': ' + $passed)
}

$records = Read-Record 'sandbox-probe-results.json'
$byId = @{}
foreach ($record in $records) { $byId[$record.id] = $record }
foreach ($pair in @(@('a-own-health',200),@('a-own-model',200),@('b-own-model',200),@('a-wrong-token',401),@('a-borrowed-b-token',403),@('b-borrowed-a-token',403),@('a-unapproved-domain',403),@('a-unapproved-host-port',403))) {
    $record = $byId[$pair[0]]
    $parsed = Parse-Probe $record.output
    Check $pair[0] ($record.exitCode -eq 0 -and $parsed.status -eq $pair[1])
}
$network = Read-Record 'final-network-log-a.json'
foreach ($pair in @(@('a-direct-ip','1.1.1.1:443'),@('a-direct-ipv6','[2606:4700:4700::1111]:443'))) {
    $parsed = Parse-Probe $byId[$pair[0]].output
    $evidence = @($network.blocked | Where-Object { $_.host -eq $pair[1] -and $_.proxy_type -eq 'transparent' })
    Check $pair[0] ($parsed.connected -eq $false -and $evidence.Count -gt 0)
}
$absent = @(Parse-Probe $byId['b-a-file-absent'].output)
Check 'a-file-not-in-b' ($absent.Count -eq 1 -and $absent[0].not_exist -eq $true)
foreach ($id in @('a-host-paths-absent','b-host-paths-absent')) {
    $record = $byId[$id]
    $probeArgs = @($record.args)
    $absentIndex = [Array]::IndexOf($probeArgs, 'absent')
    if ($absentIndex -lt 0 -or $absentIndex -ge ($probeArgs.Count - 1)) { throw ('Missing requested paths: ' + $id) }
    $requestedPaths = @($probeArgs[($absentIndex + 1)..($probeArgs.Count - 1)])
    $observations = @(Parse-Probe $record.output)
    $unexpected = @($observations | Where-Object { $_.not_exist -ne $true -or $_.path -notin $requestedPaths })
    $observedPaths = @($observations | ForEach-Object { $_.path } | Sort-Object -Unique)
    Check $id ($requestedPaths.Count -ge 2 -and $observations.Count -eq $requestedPaths.Count -and $observedPaths.Count -eq $requestedPaths.Count -and $unexpected.Count -eq 0)
}
$a = Read-Record ($SandboxA + '-inventory.json')
$b = Read-Record ($SandboxB + '-inventory.json')
Check 'different-boot-identifiers' ($a.boot_id -ne $b.boot_id -and $a.boot_id -ne '' -and $b.boot_id -ne '')
Check 'ssh-agent-not-available' ($a.ssh_socket_exists -eq $false -and $b.ssh_socket_exists -eq $false -and $a.ssh_add_exit -eq 2 -and $b.ssh_add_exit -eq 2)
$concurrency = Read-Record 'concurrency-results.json'
$running = @($concurrency.sandboxes | Where-Object { $_.status -eq 'running' })
Check 'two-guests-concurrent' ($running.Count -eq 2 -and @($concurrency.runtimeProcesses).Count -eq 2)
$lifecycle = Read-Record 'lifecycle-results.json'
$guestAfterStop = Parse-Probe $lifecycle.guest
$guestBeforeStop = Read-Record 'b-server-before-stop.json'
Check 'stop-observed-effects' ($lifecycle.hostReachableAfterStop -eq $false -and $lifecycle.hostListenerCount -eq 0 -and $guestAfterStop.server_port_open -eq $false -and $guestAfterStop.heartbeat_exists -eq $true -and $guestAfterStop.heartbeat_unchanged -eq $true -and $guestAfterStop.boot_id -ne $guestBeforeStop.boot_id)
$revoke = Read-Record 'revocation-results.json'
Check 'synthetic-token-revoked' ((Parse-Probe $revoke.fixtureTokenRevocation).status -eq 401)
Check 'scoped-network-revoked' ((Parse-Probe $revoke.scopedNetworkRevocation).status -eq 403)
$cleanup = [System.IO.File]::ReadAllText((Join-Path $root 'cleanup-results.json'), [System.Text.Encoding]::UTF8).Trim()
Check 'test-sandboxes-removed' ($cleanup -eq '[]')

$path = Join-Path $root 'acceptance-results.json'
[System.IO.File]::WriteAllText($path, ($checks | ConvertTo-Json), (New-Object System.Text.UTF8Encoding($false)))
$failed = @($checks | Where-Object { -not $_.passed })
if ($failed.Count -gt 0) { throw ('Evidence assertions failed: ' + (($failed | ForEach-Object { $_.id }) -join ',')) }
Write-Output ('Evidence assertions passed: ' + $checks.Count + '; this is not a broader safety certification.')
exit 0
