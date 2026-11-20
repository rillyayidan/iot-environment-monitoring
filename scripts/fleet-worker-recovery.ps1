# Menguji pengambilan ulang pesan pending setelah worker dihentikan paksa dan dijalankan kembali.
[CmdletBinding()]
param(
    [Parameter(Mandatory)][string]$Project,
    [Parameter(Mandatory)][string]$BaseUrl,
    [ValidateRange(15,300)][int]$RetryWatchSeconds = 60
)

$ErrorActionPreference = 'Stop'
$root = Split-Path -Parent $PSScriptRoot
$compose = @('compose','--project-directory',$root,'-p',$Project)

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
$user = Read-Setting 'POSTGRES_USER' 'iot_user'
$db = Read-Setting 'POSTGRES_DB' 'iot_monitor'
$worker = [string](Invoke-Docker ($compose + @('ps','-q','worker')))
$postgres = [string](Invoke-Docker ($compose + @('ps','-q','postgres')))
$redis = [string](Invoke-Docker ($compose + @('ps','-q','redis')))
if (-not $worker -or -not $postgres -or -not $redis) { throw 'Start a dedicated Compose stack before running the failure experiment.' }

function Invoke-SQL([string]$Query) {
    Invoke-Docker @('exec',$postgres,'psql','-X','-qAt','-v','ON_ERROR_STOP=1','-U',$user,'-d',$db,'-c',$Query)
}

function Read-Pending {
    $json = Invoke-Docker @('exec',$redis,'redis-cli','--json','XPENDING',$stream,$group,'-','+','10')
    return ,($json | ConvertFrom-Json -NoEnumerate)
}

function Read-StreamLength {
    return [int](Invoke-Docker @('exec',$redis,'redis-cli','--raw','XLEN',$stream))
}

function Wait-For([scriptblock]$Condition, [int]$Seconds, [string]$Failure) {
    $deadline = [DateTime]::UtcNow.AddSeconds($Seconds)
    do {
        if (& $Condition) { return }
        Start-Sleep -Milliseconds 300
    } while ([DateTime]::UtcNow -lt $deadline)
    throw $Failure
}

$run = [guid]::NewGuid().ToString()
$out = Join-Path $root "artifacts/fleet-recovery/$run"
New-Item -ItemType Directory -Path $out | Out-Null
$observations = [Collections.Generic.List[object]]::new()
function Add-Observation([string]$Stage, $Pending) {
    $observations.Add([pscustomobject]@{
        stage = $Stage
        at_utc = [DateTime]::UtcNow.ToString('o')
        backlog = Read-StreamLength
        pending = $Pending
    })
}

$lockSession = "fleet-recovery-$run"
$lockStarted = $false
$restorePolicy = $false
$workerStopped = $false
$policy = [string](Invoke-Docker @('inspect','--format','{{.HostConfig.RestartPolicy.Name}}',$worker))
$event = [guid]::NewGuid().ToString()
try {
    if ((Read-StreamLength) -ne 0 -or (Read-Pending).Count -ne 0) {
        throw 'The recovery experiment requires a dedicated stack with an empty stream.'
    }
    $admin = @{ Authorization = "Bearer $token" }
    $base = $BaseUrl.TrimEnd('/')
    $device = Invoke-RestMethod -Uri "$base/api/v1/devices" -Method Post -Headers $admin -ContentType 'application/json' -Body (
        @{ name = "Recovery $run"; location = 'Isolated failure test' } | ConvertTo-Json -Compress
    )
    $policies = @(
        @{ metric='co2'; threshold=1000; recovery=900; value=1350 },
        @{ metric='temperature'; threshold=30; recovery=28; value=34 },
        @{ metric='humidity'; threshold=70; recovery=65; value=80 }
    )
    $measurements = @(
        foreach ($definition in $policies) {
            $sensor = Invoke-RestMethod -Uri "$base/api/v1/devices/$($device.id)/sensors" -Method Post -Headers $admin -ContentType 'application/json' -Body (
                @{ metric=$definition.metric } | ConvertTo-Json -Compress
            )
            $null = Invoke-RestMethod -Uri "$base/api/v1/sensors/$($sensor.id)/alert-rules" -Method Post -Headers $admin -ContentType 'application/json' -Body (
                @{ operator='gte'; threshold=$definition.threshold; recovery_threshold=$definition.recovery; severity='warning' } | ConvertTo-Json -Compress
            )
            @{ sensor_id=$sensor.id; metric=$definition.metric; value=$definition.value }
        }
    )

    $lock = "SET application_name='$lockSession'; BEGIN; LOCK TABLE ingestion_events IN ACCESS EXCLUSIVE MODE; SELECT pg_sleep($($RetryWatchSeconds+30)); ROLLBACK;"
    Invoke-Docker @('exec','-d',$postgres,'psql','-X','-qAt','-U',$user,'-d',$db,'-c',$lock) | Out-Null
    $lockStarted = $true
    Wait-For {
        (Invoke-SQL "SELECT EXISTS (SELECT 1 FROM pg_locks l JOIN pg_stat_activity a USING(pid) WHERE a.application_name='$lockSession' AND l.relation='ingestion_events'::regclass AND l.mode='AccessExclusiveLock' AND l.granted)") -eq 't'
    } 10 'The PostgreSQL test lock was not acquired.'

    Invoke-Docker @('update','--restart=no',$worker) | Out-Null
    $restorePolicy = $true
    $key = @{ 'X-API-Key' = $device.api_key }
    $body = @{ event_id=$event; recorded_at=[DateTime]::UtcNow.ToString('o'); measurements=$measurements } | ConvertTo-Json -Depth 5 -Compress
    $responses = @(
        foreach ($attempt in 1..2) {
            $response = Invoke-WebRequest -Uri "$base/api/v1/readings" -Method Post -Headers $key -ContentType 'application/json' -Body $body
            [int]$response.StatusCode
        }
    )
    if ($responses.Count -ne 2 -or @($responses | Where-Object { $_ -ne 202 }).Count -ne 0) {
        throw 'Expected two HTTP 202 responses for the exact same event.'
    }
    Wait-For { (Read-Pending).Count -eq 2 } 8 'Two messages did not enter the Redis pending list.'
    $blockedQuery = "SELECT count(*) FROM pg_locks WHERE relation='ingestion_events'::regclass AND NOT granted"
    Wait-For { [int](Invoke-SQL $blockedQuery) -gt 0 } 5 'The worker did not reach the blocked PostgreSQL transaction.'
    $blockedTransactions = [int](Invoke-SQL $blockedQuery)
    $before = Read-Pending
    Add-Observation 'pending_before_kill' $before
    if (@($before | Where-Object { $_[3] -ne 1 }).Count -ne 0) { throw 'Expected an initial Redis delivery count of one.' }

    Invoke-Docker @('kill','--signal','KILL',$worker) | Out-Null
    $workerStopped = $true
    $exitCode = [int](Invoke-Docker @('inspect','--format','{{.State.ExitCode}}',$worker))
    $down = Read-Pending
    Add-Observation 'pending_after_kill' $down
    if ($exitCode -ne 137 -or $down.Count -ne 2) { throw 'SIGKILL or retained pending messages could not be confirmed.' }

    Invoke-Docker @('start',$worker) | Out-Null
    $workerStopped = $false
    $oldConsumers = @($before | ForEach-Object { $_[1] } | Sort-Object -Unique)
    $ids = @($before | ForEach-Object { $_[0] })
    Wait-For {
        $items = Read-Pending
        $reclaimed = @($items | Where-Object { $_[0] -in $ids -and $_[1] -notin $oldConsumers -and $_[3] -ge 2 })
        if ($items.Count -eq 2 -and $reclaimed.Count -eq 2) { return $true }
        return $false
    } $RetryWatchSeconds 'The replacement worker did not reclaim both pending messages.'
    $claimed = Read-Pending
    Add-Observation 'redelivered_before_commit' $claimed
    $null = Invoke-SQL "SELECT pg_cancel_backend(pid) FROM pg_stat_activity WHERE application_name='$lockSession'"
    $lockStarted = $false

    Wait-For { (Read-StreamLength) -eq 0 -and (Read-Pending).Count -eq 0 } 15 'The recovered Redis entries did not drain.'
    Add-Observation 'drained' (Read-Pending)
    $deviceID = [guid]$device.id
    $sql = @"
WITH events AS (
    SELECT e.id FROM ingestion_events e JOIN device_identifiers d ON d.device_id=e.device_id
    WHERE d.public_id='$deviceID' AND e.event_id='$event'
), persisted AS (
    SELECT s.metric,r.value FROM sensor_readings r JOIN events e ON e.id=r.ingestion_event_id
    JOIN sensors s ON s.id=r.sensor_id
), triggered AS (
    SELECT a.id,a.rule_id,a.status,a.triggered_value,r.threshold,s.metric
    FROM alerts a JOIN alert_rules r ON r.id=a.rule_id JOIN sensors s ON s.id=r.sensor_id
    JOIN device_identifiers d ON d.device_id=s.device_id WHERE d.public_id='$deviceID'
)
SELECT json_build_object(
    'events',(SELECT count(*) FROM events),
    'readings',(SELECT count(*) FROM persisted),
    'correct_readings',(SELECT count(*) FROM persisted WHERE (metric,value) IN (('co2',1350),('temperature',34),('humidity',80))),
    'alerts',(SELECT count(*) FROM triggered),
    'correct_active_alerts',(SELECT count(*) FROM triggered t JOIN persisted p USING(metric) WHERE t.status='ACTIVE' AND t.triggered_value=p.value AND t.triggered_value>=t.threshold),
    'distinct_alert_rules',(SELECT count(DISTINCT rule_id) FROM triggered)
);
"@
    $saved = Invoke-SQL $sql | ConvertFrom-Json
    $result = [pscustomobject]@{
        exit_code = $exitCode
        blocked_transactions_before_kill = $blockedTransactions
        http_statuses = $responses
        event_id = $event
        redis_message_ids = $ids
        initial_deliveries = @($before | ForEach-Object { [int]$_[3] })
        reclaimed_deliveries = @($claimed | ForEach-Object { [int]$_[3] })
        postgres = $saved
        final_backlog = Read-StreamLength
        final_pending = (Read-Pending).Count
    }
    $result | ConvertTo-Json -Depth 6 | Set-Content (Join-Path $out 'result.json')
    Write-Host "Recovery evidence saved to $out"
    $result | ConvertTo-Json -Depth 6
    if ($saved.events -ne 1 -or $saved.readings -ne 3 -or $saved.correct_readings -ne 3 -or
        $saved.alerts -ne 3 -or $saved.correct_active_alerts -ne 3 -or $saved.distinct_alert_rules -ne 3) {
        throw 'Recovery produced missing, incorrect, or duplicate database rows.'
    }
} finally {
    if ($lockStarted) { Invoke-SQL "SELECT pg_cancel_backend(pid) FROM pg_stat_activity WHERE application_name='$lockSession'" | Out-Null }
    if ($workerStopped) { Invoke-Docker @('start',$worker) | Out-Null }
    if ($restorePolicy) { Invoke-Docker @('update',"--restart=$policy",$worker) | Out-Null }
    ConvertTo-Json -InputObject ($observations.ToArray()) -Depth 8 | Set-Content (Join-Path $out 'redis-timeline.json')
}

