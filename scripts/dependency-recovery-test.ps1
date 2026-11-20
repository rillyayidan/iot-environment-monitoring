# Menguji respons API dan pemulihan data saat PostgreSQL berhenti atau Redis direstart.
[CmdletBinding()]
param(
    [Parameter(Mandatory)][string]$Project,
    [Parameter(Mandatory)][string]$BaseUrl,
    [ValidateRange(30,300)][int]$RetryWatchSeconds = 75
)

$ErrorActionPreference = 'Stop'
$root = Split-Path -Parent $PSScriptRoot
$compose = @('compose','--project-directory',$root,'-p',$Project)
$base = $BaseUrl.TrimEnd('/')

function Invoke-Docker([string[]]$Command) {
    $output = & docker @Command
    if ($LASTEXITCODE -ne 0) { throw "Docker '$($Command[0])' failed with exit code $LASTEXITCODE." }
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
$admin = @{ Authorization = "Bearer $token" }
$stream = Read-Setting 'REDIS_STREAM' 'iot:readings'
$group = Read-Setting 'REDIS_GROUP' 'iot-workers'
$user = Read-Setting 'POSTGRES_USER' 'iot_user'
$db = Read-Setting 'POSTGRES_DB' 'iot_monitor'
$services = @{}
foreach ($service in 'api','worker','postgres','redis') {
    $services[$service] = [string](Invoke-Docker ($compose + @('ps','-q',$service)))
    if (-not $services[$service]) { throw "Start the dedicated Compose service $service." }
}
$postgres = $services.postgres
$redis = $services.redis

function Invoke-SQL([string]$Query) {
    Invoke-Docker @('exec',$postgres,'psql','-X','-qAt','-v','ON_ERROR_STOP=1','-U',$user,'-d',$db,'-c',$Query)
}

function Wait-For([scriptblock]$Condition, [int]$Seconds, [string]$Failure) {
    $deadline = [DateTime]::UtcNow.AddSeconds($Seconds)
    do {
        if (& $Condition) { return }
        Start-Sleep -Milliseconds 300
    } while ([DateTime]::UtcNow -lt $deadline)
    throw $Failure
}

function Read-Queue {
    $lua = 'return {redis.call("XLEN",KEYS[1]), redis.call("XPENDING",KEYS[1],ARGV[1],"-","+",10)}'
    $state = Invoke-Docker @('exec',$redis,'redis-cli','--json','EVAL',$lua,'1',$stream,$group) |
        ConvertFrom-Json -NoEnumerate
    if ($state.Count -ne 2) { throw 'Could not read Redis stream and pending entries.' }
    [pscustomobject]@{ at_utc=[DateTime]::UtcNow.ToString('o'); backlog=[int]$state[0]; pending=@($state[1]) }
}

function Queue-IDs($Snapshot) {
    @($Snapshot.pending | ForEach-Object { $_[0] } | Sort-Object) -join ','
}

function Read-Health {
    $live = Invoke-WebRequest -Uri "$base/health/live" -SkipHttpErrorCheck -TimeoutSec 8
    $ready = Invoke-WebRequest -Uri "$base/health/ready" -SkipHttpErrorCheck -TimeoutSec 8
    [pscustomobject]@{ live=[int]$live.StatusCode; ready=[int]$ready.StatusCode; retry_after=[string]$ready.Headers['Retry-After'] }
}

function Register-Probe([string]$Name) {
    $device = Invoke-RestMethod -Uri "$base/api/v1/devices" -Method Post -Headers $admin -ContentType 'application/json' -Body (
        @{ name=$Name; location='Dependency recovery test' } | ConvertTo-Json -Compress
    )
    $definitions = @(
        @{ metric='co2'; threshold=1000; recovery=900; high=1350; normal=700 },
        @{ metric='temperature'; threshold=30; recovery=28; high=34; normal=24 },
        @{ metric='humidity'; threshold=70; recovery=65; high=80; normal=52 }
    )
    $high = @()
    $normal = @()
    foreach ($setting in $definitions) {
        $sensor = Invoke-RestMethod -Uri "$base/api/v1/devices/$($device.id)/sensors" -Method Post -Headers $admin -ContentType 'application/json' -Body (
            @{ metric=$setting.metric } | ConvertTo-Json -Compress
        )
        $null = Invoke-RestMethod -Uri "$base/api/v1/sensors/$($sensor.id)/alert-rules" -Method Post -Headers $admin -ContentType 'application/json' -Body (
            @{ operator='gte'; threshold=$setting.threshold; recovery_threshold=$setting.recovery; severity='warning' } | ConvertTo-Json -Compress
        )
        $high += @{ sensor_id=$sensor.id; metric=$setting.metric; value=$setting.high }
        $normal += @{ sensor_id=$sensor.id; metric=$setting.metric; value=$setting.normal }
    }
    return @{ id=[guid]$device.id; header=@{'X-API-Key'=$device.api_key}; high=$high; normal=$normal }
}

function New-Body([guid]$Id, [object[]]$Measurements) {
    @{ event_id=$Id.ToString(); recorded_at=[DateTime]::UtcNow.ToString('o'); measurements=$Measurements } |
        ConvertTo-Json -Depth 5 -Compress
}

function Send-Reading([hashtable]$Headers, [string]$Body) {
    $reply = Invoke-WebRequest -Uri "$base/api/v1/readings" -Method Post -Headers $Headers -ContentType 'application/json' -Body $Body -SkipHttpErrorCheck -TimeoutSec 12
    [pscustomobject]@{ status=[int]$reply.StatusCode; retry_after=[string]$reply.Headers['Retry-After']; body=($reply.Content | ConvertFrom-Json) }
}

function Read-Persistence([guid]$Device, [guid]$High, [guid]$Interrupted, [guid]$Recovery) {
    $query = @"
WITH selected AS (
    SELECT device_id FROM device_identifiers WHERE public_id='$Device'
), events AS (
    SELECT e.id,e.event_id,e.recorded_at FROM ingestion_events e JOIN selected d USING(device_id)
), samples AS (
    SELECT e.event_id,r.recorded_at,e.recorded_at AS event_at,s.metric,r.value
    FROM sensor_readings r JOIN events e ON e.id=r.ingestion_event_id JOIN sensors s ON s.id=r.sensor_id
), activity AS (
    SELECT a.*,r.threshold,r.recovery_threshold,s.metric
    FROM alerts a JOIN alert_rules r ON r.id=a.rule_id JOIN sensors s ON s.id=r.sensor_id
    JOIN selected d ON d.device_id=s.device_id
)
SELECT json_build_object(
    'events',(SELECT count(*) FROM events),
    'before',(SELECT count(*) FROM events WHERE event_id='$High'),
    'during',(SELECT count(*) FROM events WHERE event_id='$Interrupted'),
    'after',(SELECT count(*) FROM events WHERE event_id='$Recovery'),
    'readings',(SELECT count(*) FROM samples),
    'matching_readings',(SELECT count(*) FROM samples WHERE recorded_at=event_at AND (
        (event_id='$High' AND (metric,value) IN (('co2',1350),('temperature',34),('humidity',80))) OR
        (event_id='$Recovery' AND (metric,value) IN (('co2',700),('temperature',24),('humidity',52)))
    )),
    'alerts',(SELECT count(*) FROM activity),
    'alert_rules',(SELECT count(DISTINCT rule_id) FROM activity),
    'active',(SELECT count(*) FROM activity WHERE status='ACTIVE'),
    'resolved',(SELECT count(*) FROM activity WHERE status='RESOLVED' AND resolved_at>triggered_at),
    'valid_transitions',(SELECT count(*) FROM activity a
        JOIN samples h ON h.event_id='$High' AND h.metric=a.metric AND h.recorded_at=a.triggered_at
        JOIN samples n ON n.event_id='$Recovery' AND n.metric=a.metric AND n.recorded_at=a.resolved_at
        WHERE h.value=a.triggered_value AND h.value>=a.threshold AND n.value<=a.recovery_threshold),
    'dead_letters',(SELECT count(*) FROM ingestion_rejections)
);
"@
    Invoke-SQL $query | ConvertFrom-Json
}

function Run-DependencyScenario([string]$Dependency) {
    $run = [guid]::NewGuid().ToString()
    $out = Join-Path $root "artifacts/dependency-recovery/$Dependency-$run"
    New-Item -ItemType Directory -Path $out | Out-Null
    $proof = [ordered]@{ dependency=$Dependency; run=$run }
    $session = "dependency-test-$run"
    $lockStarted = $false
    $stopped = $false
    $target = $services[$Dependency]
    try {
        $proof.initial_health = Read-Health
        if ($proof.initial_health.live -ne 200 -or $proof.initial_health.ready -ne 200 -or (Read-Queue).backlog -ne 0) {
            throw 'Start with a healthy dedicated stack and an empty stream.'
        }
        $probe = Register-Probe "$Dependency recovery $run"
        $appStart = @{}
        foreach ($service in 'api','worker') {
            $appStart[$service] = Invoke-Docker @('inspect','--format','{{.State.StartedAt}}',$services[$service])
        }
        $high = [guid]::NewGuid()
        $during = [guid]::NewGuid()
        $recovery = [guid]::NewGuid()
        $proof.event_ids = @{ before=$high; during=$during; after=$recovery }
        $lock = "SET application_name='$session'; BEGIN; LOCK TABLE ingestion_events IN ACCESS EXCLUSIVE MODE; SELECT pg_sleep($($RetryWatchSeconds+35)); ROLLBACK;"
        Invoke-Docker @('exec','-d',$postgres,'psql','-X','-qAt','-U',$user,'-d',$db,'-c',$lock) | Out-Null
        $lockStarted = $true
        Wait-For {
            (Invoke-SQL "SELECT EXISTS (SELECT 1 FROM pg_locks l JOIN pg_stat_activity a USING(pid) WHERE a.application_name='$session' AND l.relation='ingestion_events'::regclass AND l.mode='AccessExclusiveLock' AND l.granted)") -eq 't'
        } 10 'The PostgreSQL test lock was not acquired.'

        $body = New-Body $high $probe.high
        $proof.before_http = @((Send-Reading $probe.header $body),(Send-Reading $probe.header $body))
        if (@($proof.before_http | Where-Object status -ne 202).Count) { throw 'Expected two pre-outage HTTP 202 responses.' }
        Wait-For { (Read-Queue).pending.Count -eq 2 } 8 'Expected two in-flight messages.'
        $blocking = "SELECT count(*) FROM pg_locks WHERE relation='ingestion_events'::regclass AND NOT granted"
        Wait-For { [int](Invoke-SQL $blocking) -gt 0 } 5 'The worker did not reach the blocked PostgreSQL transaction.'
        $proof.blocked_transactions = [int](Invoke-SQL $blocking)
        $proof.before_queue = Read-Queue
        $ids = Queue-IDs $proof.before_queue
        if ($Dependency -eq 'redis') {
            $proof.redis_aof = Invoke-Docker @('exec',$redis,'redis-cli','--json','CONFIG','GET','appendonly','appendfsync') | ConvertFrom-Json
            Start-Sleep -Seconds 2
        }

        Write-Host "Stopping $Dependency with two Redis messages pending."
        Invoke-Docker @('stop','--time','10',$target) | Out-Null
        $stopped = $true
        $downAt = [DateTime]::UtcNow
        $proof.stopped_at_utc = $downAt.ToString('o')
        $proof.dependency_running_during = (Invoke-Docker @('inspect','--format','{{.State.Running}}',$target)) -eq 'true'
        $proof.during_health = Read-Health
        $proof.during_http = Send-Reading $probe.header (New-Body $during $probe.normal)
        if ($proof.dependency_running_during -or $proof.during_health.live -ne 200 -or
            $proof.during_health.ready -ne 503 -or $proof.during_health.retry_after -ne '2' -or
            $proof.during_http.status -ne 503 -or $proof.during_http.retry_after -ne '2') {
            throw 'Expected liveness 200 and HTTP 503 with Retry-After: 2 for readiness and ingestion.'
        }
        if ($Dependency -eq 'postgres') {
            $heartbeat = Invoke-WebRequest -Uri "$base/api/v1/devices/heartbeat" -Method Post -Headers $probe.header -SkipHttpErrorCheck -TimeoutSec 12
            $proof.during_heartbeat = @{ status=[int]$heartbeat.StatusCode; retry_after=[string]$heartbeat.Headers['Retry-After'] }
            if ($proof.during_heartbeat.status -ne 503 -or $proof.during_heartbeat.retry_after -ne '2') {
                throw 'Expected HTTP 503 with Retry-After: 2 during device API Key authentication.'
            }
            $adminRead = Invoke-WebRequest -Uri "$base/api/v1/devices/$($probe.id)" -Headers $admin -SkipHttpErrorCheck -TimeoutSec 12
            $proof.during_admin_read = @{ status=[int]$adminRead.StatusCode; retry_after=[string]$adminRead.Headers['Retry-After'] }
            if ($proof.during_admin_read.status -ne 503 -or $proof.during_admin_read.retry_after -ne '2') {
                throw 'Expected HTTP 503 with Retry-After: 2 on database-backed admin reads.'
            }
            $proof.during_queue = Read-Queue
            if ($proof.during_queue.backlog -ne 2 -or (Queue-IDs $proof.during_queue) -ne $ids) {
                throw 'Redis did not retain both pending messages while PostgreSQL was stopped.'
            }
        }

        Start-Sleep -Seconds 2
        Invoke-Docker @('start',$target) | Out-Null
        $stopped = $false
        if ($Dependency -eq 'postgres') { $lockStarted = $false }
        $proof.restarted_at_utc = [DateTime]::UtcNow.ToString('o')
        Wait-For { (Read-Health).ready -eq 200 } 20 'API readiness did not recover.'
        $proof.after_health = Read-Health
        $proof.seconds_from_stop_to_ready = [Math]::Round(([DateTime]::UtcNow-$downAt).TotalSeconds,3)

        if ($Dependency -eq 'redis') {
            $proof.redis_after_restart = Read-Queue
            if ($proof.redis_after_restart.backlog -ne 2 -or (Queue-IDs $proof.redis_after_restart) -ne $ids) {
                throw 'The Redis restart did not restore both pending message IDs.'
            }
            Wait-For {
                $queue = Read-Queue
                (Queue-IDs $queue) -eq $ids -and @($queue.pending | Where-Object { $_[3] -ge 2 }).Count -eq 2
            } $RetryWatchSeconds 'The worker did not re-deliver both restored pending messages.'
            $proof.redis_after_reclaim = Read-Queue
            $null = Invoke-SQL "SELECT pg_cancel_backend(pid) FROM pg_stat_activity WHERE application_name='$session'"
            $lockStarted = $false
        }

        Wait-For { (Read-Queue).backlog -eq 0 } $RetryWatchSeconds 'Pre-outage entries did not drain.'
        $proof.drained_queue = Read-Queue
        $proof.drained_before_post = Read-Persistence $probe.id $high $during $recovery
        $early = $proof.drained_before_post
        if ($proof.drained_queue.pending.Count -ne 0 -or $early.events -ne 1 -or $early.before -ne 1 -or
            $early.during -ne 0 -or $early.readings -ne 3 -or $early.matching_readings -ne 3 -or
            $early.alerts -ne 3 -or $early.active -ne 3) { throw 'Pre-outage data failed reconciliation.' }

        $proof.after_http = Send-Reading $probe.header (New-Body $recovery $probe.normal)
        if ($proof.after_http.status -ne 202) { throw 'Expected HTTP 202 for fresh traffic after recovery.' }
        Wait-For { (Read-Queue).backlog -eq 0 } 15 'Post-recovery entry did not drain.'
        $proof.final_queue = Read-Queue
        $proof.postgres = Read-Persistence $probe.id $high $during $recovery
        $proof.services_restarted = @(
            foreach ($service in 'api','worker') {
                $current = Invoke-Docker @('inspect','--format','{{.State.StartedAt}}',$services[$service])
                if ($current -ne $appStart[$service]) { $service }
            }
        )
        $final = $proof.postgres
        if ($proof.final_queue.pending.Count -ne 0 -or $proof.services_restarted.Count -ne 0 -or
            $final.events -ne 2 -or $final.before -ne 1 -or $final.after -ne 1 -or $final.during -ne 0 -or
            $final.readings -ne 6 -or $final.matching_readings -ne 6 -or $final.alerts -ne 3 -or
            $final.alert_rules -ne 3 -or $final.active -ne 0 -or $final.resolved -ne 3 -or
            $final.valid_transitions -ne 3 -or $final.dead_letters -ne 0) {
            throw 'Final PostgreSQL, Redis, or service state failed reconciliation.'
        }
        $proof.passed = $true
        Write-Host "Evidence saved to $out"
        $proof | ConvertTo-Json -Depth 10
    } catch {
        $proof.failure = $_.Exception.Message
        throw
    } finally {
        if ($stopped) { Invoke-Docker @('start',$target) | Out-Null }
        if ($lockStarted -and -not ($stopped -and $Dependency -eq 'postgres')) {
            $null = Invoke-SQL "SELECT pg_cancel_backend(pid) FROM pg_stat_activity WHERE application_name='$session'"
        }
        $proof | ConvertTo-Json -Depth 10 | Set-Content (Join-Path $out 'result.json')
    }
}

$failures = @()
foreach ($dependency in 'postgres','redis') {
    try { Run-DependencyScenario $dependency }
    catch {
        $failures += $dependency
        Write-Warning $_.Exception.Message
    }
}
if ($failures.Count) { throw ('Failed dependency scenarios: ' + ($failures -join ', ')) }

