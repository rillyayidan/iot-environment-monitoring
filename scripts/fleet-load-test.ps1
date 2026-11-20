# Menjalankan stress test banyak perangkat, memantau Redis, dan memverifikasi hasil di PostgreSQL.
[CmdletBinding()]
param(
    [Parameter(Mandatory)][string]$Project,
    [ValidateSet(100,500,1000)][int[]]$Devices = @(100,500,1000),
    [ValidateRange(60, 300)][int]$Seconds = 60,
    [ValidateRange(15, 600)][int]$DrainSeconds = 120,
    [string]$K6Image = 'grafana/k6:1.4.2'
)

$ErrorActionPreference = 'Stop'
$root = Split-Path -Parent $PSScriptRoot
$compose = @('compose', '--project-directory', $root, '-p', $Project)

function Invoke-Docker([string[]]$Command) {
    $output = & docker @Command
    if ($LASTEXITCODE -ne 0) { throw "Docker operation '$($Command[0])' failed with exit code $LASTEXITCODE." }
    return $output
}

function Read-Setting([string]$Name, [string]$Default) {
    $value = [Environment]::GetEnvironmentVariable($Name, 'Process')
    if ($value) { return $value }
    $file = Join-Path $root '.env'
    if (Test-Path -LiteralPath $file) {
        $line = Get-Content -LiteralPath $file | Where-Object { $_.StartsWith("$Name=") } | Select-Object -First 1
        if ($line) { return $line.Substring($Name.Length + 1) }
    }
    return $Default
}

$token = Read-Setting 'ADMIN_TOKEN' ''
if (-not $token) { throw 'Set ADMIN_TOKEN or configure it in .env.' }
$stream = Read-Setting 'REDIS_STREAM' 'iot:readings'
$group = Read-Setting 'REDIS_GROUP' 'iot-workers'
$postgres = [string](Invoke-Docker ($compose + @('ps', '-q', 'postgres')))
$redis = [string](Invoke-Docker ($compose + @('ps', '-q', 'redis')))
if (-not $postgres -or -not $redis) { throw 'Start the isolated Compose stack before running the benchmark.' }
$user = Read-Setting 'POSTGRES_USER' 'iot_user'
$database = Read-Setting 'POSTGRES_DB' 'iot_monitor'

function Get-QueueSample([string]$Phase) {
    $sample = 'local p = redis.call("XPENDING", KEYS[1], ARGV[1]); return {redis.call("XLEN", KEYS[1]), p[1]}'
    $queue = Invoke-Docker @('exec',$redis,'redis-cli','--json','EVAL',$sample,'1',$stream,$group) | ConvertFrom-Json -NoEnumerate
    if ($queue.Count -ne 2) { throw 'Could not read Redis backlog and consumer group state.' }
    return [pscustomobject]@{
        time_utc = [DateTime]::UtcNow.ToString('o')
        phase = $Phase
        backlog = [int]$queue[0]
        pending = [int]$queue[1]
    }
}

function Save-Output([string]$Container, [string]$From, [string]$To) {
    Invoke-Docker @('cp', "${Container}:$From", $To) | Out-Null
}

function Invoke-FleetScenario([int]$Count) {
    $runID = [guid]::NewGuid().ToString()
    $out = Join-Path $root "artifacts/fleet-load/$Count-$runID"
    New-Item -ItemType Directory -Path $out | Out-Null
    $samples = [Collections.Generic.List[object]]::new()
    $runner = $null
    $previousToken = $env:ADMIN_TOKEN
    try {
        $initial = Get-QueueSample 'before'
        if ($initial.backlog -ne 0 -or $initial.pending -ne 0) {
            throw 'Use a dedicated Compose stack with an empty stream and pending list.'
        }
        $samples.Add($initial)
        $runner = [string](Invoke-Docker @(
            'run','-d','--entrypoint','/bin/sh','--network',"${Project}_default",'-u','0:0',
            '--mount',"type=bind,source=$PSScriptRoot,target=/work,readonly",
            '-e',"FLEET_DEVICES=$Count",'-e',"FLEET_RUN_ID=$runID",'-e',"FLEET_DURATION=${Seconds}s",
            $K6Image,'-c','sleep 1200'
        ))
        $env:ADMIN_TOKEN = $token
        Write-Host "Provisioning $Count devices and $($Count * 3) sensors and rules."
        $provision = 'k6 --log-format raw run --quiet --console-output /tmp/fleet-secrets.ndjson /work/fleet-provision.js > /tmp/provision.log 2>&1'
        & docker exec -e ADMIN_TOKEN $runner /bin/sh -c $provision
        $provisionExit = $LASTEXITCODE
        $env:ADMIN_TOKEN = $previousToken
        Save-Output $runner '/tmp/provision.log' (Join-Path $out 'provision.log')
        if ($provisionExit -ne 0) { throw "Fleet provisioning failed. See $out\provision.log." }

        Write-Host "Running $Count devices concurrently for $Seconds seconds."
        $run = 'k6 --log-format raw run --quiet --console-output /tmp/accepted.csv --out csv=/tmp/k6-points.csv /work/fleet-ingest.js > /tmp/k6.log 2>&1; rc=$?; printf "%s\n" "$rc" > /tmp/k6.exit'
        Invoke-Docker @('exec','-d',$runner,'/bin/sh','-c',$run) | Out-Null
        $limit = [DateTime]::UtcNow.AddSeconds($Seconds + 45)
        do {
            $samples.Add((Get-QueueSample 'load'))
            & docker exec $runner test -f /tmp/k6.exit
            if ($LASTEXITCODE -eq 0) { break }
            if ([DateTime]::UtcNow -ge $limit) { throw 'k6 did not finish before the watchdog deadline.' }
            Start-Sleep -Milliseconds 800
        } while ($true)

        $k6Exit = Invoke-Docker @('exec',$runner,'cat','/tmp/k6.exit')
        Save-Output $runner '/tmp/k6.log' (Join-Path $out 'k6.log')
        if ($k6Exit -ne '0') { throw "k6 failed. See $out\k6.log." }
        $drainStart = [DateTime]::UtcNow
        do {
            $final = Get-QueueSample 'drain'
            $samples.Add($final)
            if ($final.backlog -eq 0 -and $final.pending -eq 0) { break }
            if ([DateTime]::UtcNow -ge $drainStart.AddSeconds($DrainSeconds)) { break }
            Start-Sleep -Milliseconds 800
        } while ($true)
        $drainTime = ([DateTime]::UtcNow - $drainStart).TotalSeconds

        Invoke-Docker @('exec',$runner,'gzip','/tmp/k6-points.csv') | Out-Null
        Save-Output $runner '/tmp/k6-points.csv.gz' (Join-Path $out 'k6-points.csv.gz')
        Save-Output $runner '/tmp/k6.json' (Join-Path $out 'k6.json')
        Save-Output $runner '/tmp/devices.json' (Join-Path $out 'devices.json')
        $ledger = Join-Path $out 'accepted.csv'
        Save-Output $runner '/tmp/accepted.csv' $ledger
        $summary = Get-Content -Raw (Join-Path $out 'k6.json') | ConvertFrom-Json

        $ledgerFile = "/tmp/fleet-$runID.csv"
        Invoke-Docker @('cp',$ledger,"${postgres}:$ledgerFile") | Out-Null
        Invoke-Docker @('cp',(Join-Path $PSScriptRoot 'verify-fleet.sql'),"${postgres}:/tmp/verify-fleet.sql") | Out-Null
        $storedJson = Invoke-Docker @(
            'exec',$postgres,'psql','-X','-qAt','-U',$user,'-d',$database,
            '-v',"ledger_file=$ledgerFile",'-v',"run_id=$runID",'-f','/tmp/verify-fleet.sql'
        )
        $stored = $storedJson | ConvertFrom-Json
        $expected = [int]$summary.metrics.accepted_unique_events.values.count
        $expectedEachState = 3 * ($Count / 5)
        $result = [pscustomobject]@{
            devices = $Count
            nominal_seconds = $Seconds
            k6_elapsed_seconds = $summary.state.testRunDurationMs / 1000
            requests = $summary.metrics.http_reqs.values.count
            requests_per_second = $summary.metrics.http_reqs.values.rate
            p95_ms = $summary.metrics.http_req_duration.values.'p(95)'
            p99_ms = $summary.metrics.http_req_duration.values.'p(99)'
            error_rate = $summary.metrics.http_req_failed.values.rate
            status_503 = $summary.metrics.status_503.values.count
            transport_errors = $summary.metrics.transport_errors.values.count
            accepted_requests = $summary.metrics.accepted_requests.values.count
            accepted_unique_events = $expected
            replay_attempts = $summary.metrics.replay_attempts.values.count
            confirmed_replays = $summary.metrics.confirmed_replays.values.count
            peak_sampled_backlog = ($samples | Measure-Object backlog -Maximum).Maximum
            peak_sampled_pending = ($samples | Measure-Object pending -Maximum).Maximum
            final_backlog = $final.backlog
            final_pending = $final.pending
            sampled_drain_seconds = [Math]::Round($drainTime,3)
            reconciliation = $stored
        }
        $result | ConvertTo-Json -Depth 6 | Set-Content (Join-Path $out 'result.json')
        Write-Host "Results saved to $out"
        $result | ConvertTo-Json -Depth 6
        if ($expected -eq 0 -or $stored.accepted_ledger -ne $expected -or
            $stored.devices -ne $Count -or $stored.active_keys -ne $Count -or $stored.devices_with_one_key -ne $Count -or
            $stored.devices_sending -ne $Count -or $stored.devices_with_three_metrics -ne $Count -or
            $stored.sensors -ne 3*$Count -or $stored.rules -ne 3*$Count -or $stored.sensors_with_one_enabled_rule -ne 3*$Count -or
            $stored.persisted_events -ne $expected -or $stored.persisted_readings -ne 3*$expected -or
            $stored.missing_events -ne 0 -or $stored.unexpected_events -ne 0 -or
            $stored.reading_mismatches -ne 0 -or $stored.duplicate_readings -ne 0 -or
            $stored.active_alerts -ne $expectedEachState -or $stored.resolved_alerts -ne $expectedEachState -or
            $stored.expected_active_alerts -ne $expectedEachState -or $stored.expected_resolved_alerts -ne $expectedEachState -or
            $stored.duplicate_alerts -ne 0 -or $stored.alert_state_mismatches -ne 0 -or $stored.invalid_alert_readings -ne 0 -or
            $stored.dead_letters -ne 0 -or $final.backlog -ne 0 -or $final.pending -ne 0 -or $result.confirmed_replays -eq 0) {
            throw "Fleet reconciliation failed for $Count devices. See $out\result.json."
        }
    } finally {
        $env:ADMIN_TOKEN = $previousToken
        ConvertTo-Json -InputObject ($samples.ToArray()) -Depth 5 | Set-Content (Join-Path $out 'redis-samples.json')
        if ($runner) { & docker rm -f $runner | Out-Null }
    }
}

$failed = @()
foreach ($count in $Devices) {
    try { Invoke-FleetScenario $count }
    catch {
        Write-Warning $_.Exception.Message
        $failed += $count
    }
}
if ($failed.Count -gt 0) { throw "Failed scenarios: $($failed -join ', ')." }

