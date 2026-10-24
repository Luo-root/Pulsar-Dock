param(
    [string]$TracePath = (Join-Path $PSScriptRoot '../../validation/2026-10-24/claude-guest-trace.jsonl'),
    [string]$OutputPath = (Join-Path $PSScriptRoot '../../validation/2026-10-24/claude-mcp-tool-evidence.json')
)
$ErrorActionPreference = 'Stop'
# Transform recorded JSONL into selected observable evidence. Do not export
# thinking blocks, hooks, model credentials, or unrelated session information.
$events = @()
$final = $null
$lines = [System.IO.File]::ReadAllLines([System.IO.Path]::GetFullPath($TracePath), [System.Text.Encoding]::UTF8)
foreach ($line in $lines) {
    if ([string]::IsNullOrWhiteSpace($line)) { continue }
    $entry = $line | ConvertFrom-Json
    if ($entry.type -eq 'result') {
        $final = $entry
        continue
    }
    if ($entry.type -notin @('assistant','user')) { continue }
    foreach ($block in @($entry.message.content)) {
        if ($null -eq $block) { continue }
        if ($block.type -eq 'tool_use') {
            $events += [pscustomobject]@{type='tool_use'; id=$block.id; name=$block.name; input=$block.input}
        } elseif ($block.type -eq 'tool_result') {
            $parsed = $null
            if ($entry.tool_use_result.structuredContent) {
                $parsed = $entry.tool_use_result.structuredContent
            } elseif ($block.content -is [string]) {
                $parsed = $block.content | ConvertFrom-Json
            }
            $events += [pscustomobject]@{type='tool_result'; tool_use_id=$block.tool_use_id; is_error=($block.is_error -eq $true); result=$parsed}
        }
    }
}
$create = @($events | Where-Object { $_.type -eq 'tool_use' -and $_.name -eq 'mcp__dock__task_create_child' })
$status = @($events | Where-Object { $_.type -eq 'tool_use' -and $_.name -eq 'mcp__dock__task_status' })
if ($create.Count -ne 1 -or $status.Count -ne 1) { throw 'Expected exactly one create and one status tool call.' }
$created = @($events | Where-Object { $_.type -eq 'tool_result' -and $_.tool_use_id -eq $create[0].id })
$queried = @($events | Where-Object { $_.type -eq 'tool_result' -and $_.tool_use_id -eq $status[0].id })
if ($created.Count -ne 1 -or $queried.Count -ne 1) { throw 'Both calls must have a matching tool_result.' }
if ($created[0].is_error -or $queried[0].is_error) { throw 'Tool result was an error.' }
if ($create[0].input.goal -ne 'trace-check-20261024') { throw 'Create goal was not preserved.' }
$taskID = $created[0].result.task_id
if ($taskID -ne 'task-0001' -or $status[0].input.task_id -ne $taskID -or $queried[0].result.task_id -ne $taskID) { throw 'Status did not query the created task.' }
if ($queried[0].result.note -ne $create[0].input.goal -or $created[0].result.state -ne 'created' -or $queried[0].result.state -ne 'created') { throw 'Task state/goal did not match.' }
if ($null -eq $final -or $final.is_error -ne $false -or $final.terminal_reason -ne 'completed') { throw 'Claude did not complete normally.' }
$summary = [ordered]@{
    date = '2026-10-24'
    result = 'passed'
    evidence_level = 'recorded Claude stream-json tool_use and matching tool_result; not just final text'
    model = 'deepseek/deepseek-flash[1m] via host Magpie'
    runtime = 'sbx v0.49.2 local microVM, no workspace, shared skills off'
    calls = $events
    assertions = @('exactly one create call','exactly one status call','both calls have matching successful results','goal preserved','same task ID queried','Claude completed normally')
    scope = 'In-memory task record only. No child VM/Agent was created; no artifact was validated or published.'
    cli_reported_cost_usd = $final.total_cost_usd
    cost_note = 'CLI estimate only; provider costBasis was unknown, actual billing not verified'
}
$destination = [System.IO.Path]::GetFullPath($OutputPath)
[System.IO.File]::WriteAllText($destination, ($summary | ConvertTo-Json -Depth 10), (New-Object System.Text.UTF8Encoding($false)))
Write-Output ('Claude MCP evidence assertions PASS; recorded calls=' + @($events | Where-Object {$_.type -eq 'tool_use'}).Count)
Write-Output ('Selected evidence: ' + $destination)
exit 0
