param(
    [string]$SbxPath = 'sbx',
    [Parameter(Mandatory=$true)][string]$EvidenceRoot
)

# Read-only sbx capability checks. Does not log in, create sandboxes,
# change policy, read secrets, or reset the user's global configuration.
$ErrorActionPreference = 'Stop'
$evidence = [System.IO.Path]::GetFullPath($EvidenceRoot)
[System.IO.Directory]::CreateDirectory($evidence) | Out-Null
$results = @()

$queries = @(
    @{Name='version'; Args=@('version')},
    @{Name='top-help'; Args=@('--help')},
    @{Name='create-help'; Args=@('create','--help')},
    @{Name='exec-help'; Args=@('exec','--help')},
    @{Name='copy-help'; Args=@('cp','--help')},
    @{Name='policy-help'; Args=@('policy','--help')},
    @{Name='login-help'; Args=@('login','--help')},
    @{Name='daemon-status'; Args=@('daemon','status')}
)

foreach ($query in $queries) {
    $queryArgs = $query.Args
    $previousPreference = $ErrorActionPreference
    $ErrorActionPreference = 'Continue'
    try {
        $text = & $SbxPath @queryArgs 2>&1
        $code = $LASTEXITCODE
    } finally { $ErrorActionPreference = $previousPreference }
    $output = ($text | ForEach-Object { [string]$_ }) -join [Environment]::NewLine
    $outputFile = Join-Path $evidence ('sbx-' + $query.Name + '.txt')
    [System.IO.File]::WriteAllText($outputFile, $output, (New-Object System.Text.UTF8Encoding($false)))
    $results += [pscustomobject]@{Check=$query.Name; ExitCode=$code; OutputFile=$outputFile}
    Write-Output ($query.Name + ' exit=' + $code)
    if ($query.Name -eq 'daemon-status') { Write-Output $output }
}

$resultsFile = Join-Path $evidence 'sbx-cli-results.json'
[System.IO.File]::WriteAllText($resultsFile, ($results | ConvertTo-Json -Depth 4), (New-Object System.Text.UTF8Encoding($false)))
Write-Output ('Results: ' + $resultsFile)
