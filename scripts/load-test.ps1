# Menyiapkan dan menjalankan stress test k6 pada satu perangkat serta menyimpan metrik dan hasil verifikasi.
[CmdletBinding()]
param(
    [ValidateSet('100','500','1000','2000')][string]$VUs = '100',
    [ValidatePattern('^[1-9][0-9]*[sm]$')][string]$Duration = '30s',
    [ValidateRange(0, 20000)][int]$Rate = 0,
    [string]$Project = 'iot-environment-monitoring',
    [string]$BaseUrl = 'http://127.0.0.1:8080',
    [ValidateRange(5, 600)][int]$DrainSeconds = 120,
    [string]$K6Image = 'grafana/k6:1.4.2'
)

$ErrorActionPreference = 'Stop'
$root = Split-Path -Parent $PSScriptRoot
$runId = [guid]::NewGuid().ToString()
$out = Join-Path $root "artifacts/load/$VUs-$runId"
$null = New-Item -ItemType Directory -Path $out
$samples = [Collections.Generic.List[object]]::new()
$container = $null
$compose = @('compose', '--project-directory', $root, '-p', $Project)
$dotenv = Join-Path $root '.env'

function Read-Setting([string]$Name, [string]$Fallback) {
    $value = [Environment]::GetEnvironmentVariable($Name, 'Process')
    if ($value) { return $value }
    if (Test-Path $dotenv) {
        $line = Get-Content $dotenv | Where-Object { $_.StartsWith("$Name=") } | Select-Object -First 1
        if ($line) { return $line.Substring($Name.Length + 1) }
    }
    return $Fallback
}

$token = Read-Setting 'ADMIN_TOKEN' ''
if (-not $token) { throw 'Set ADMIN_TOKEN or configure it in .env.' }
$streamName = Read-Setting 'REDIS_STREAM' 'iot:readings'

function Get-QueueDepth {
    $depth = & docker @compose exec -T redis redis-cli XLEN $streamName
    if ($LASTEXITCODE -ne 0 -or $depth -notmatch '^\d+$') { throw 'Unable to measure Redis stream backlog.' }
    return [int]$depth
}

function Add-Sample {
    $services = @(& docker @compose ps -q api worker postgres redis)
    if ($LASTEXITCODE -ne 0 -or $services.Count -lt 4) { throw 'Compose API, worker, PostgreSQL, and Redis must be running.' }
    $depth = Get-QueueDepth
    $resources = @(& docker stats --no-stream --format '{{json .}}' @services | ForEach-Object { ConvertFrom-Json $_ })
    if ($LASTEXITCODE -ne 0) { Write-Warning 'Docker resource sample was unavailable.' }
    $samples.Add([pscustomobject]@{ time_utc = [DateTime]::UtcNow.ToString('o'); queue_backlog = $depth; containers = $resources })
}

$variables = @('LOAD_VUS','LOAD_DURATION','LOAD_RATE','LOAD_RUN_ID','LOAD_DEVICE_KEY','LOAD_SENSOR_ID')
$previous = @{}
foreach ($variable in $variables) { $previous[$variable] = [Environment]::GetEnvironmentVariable($variable, 'Process') }
try {
    $headers = @{ Authorization = "Bearer $token" }
    $device = Invoke-RestMethod -Method Post -Uri "$BaseUrl/api/v1/devices" -Headers $headers -ContentType 'application/json' -Body (
        @{ name = "load-$VUs-$runId"; location = 'isolated load test' } | ConvertTo-Json -Compress
    )
    $deviceID = [guid]$device.id
    $sensor = Invoke-RestMethod -Method Post -Uri "$BaseUrl/api/v1/devices/$deviceID/sensors" -Headers $headers -ContentType 'application/json' -Body '{"metric":"co2"}'
    $env:LOAD_VUS = $VUs
    $env:LOAD_DURATION = $Duration
    $env:LOAD_RATE = [string]$Rate
    $env:LOAD_RUN_ID = $runId
    $env:LOAD_DEVICE_KEY = $device.api_key
    $env:LOAD_SENSOR_ID = $sensor.id

    Add-Sample
    $dockerArgs = @(
        'run', '-d', '--network', "${Project}_default", '-u', '0:0',
        '--mount', "type=bind,source=$PSScriptRoot,target=/work,readonly",
        '--mount', "type=bind,source=$out,target=/out",
        '-e', 'LOAD_VUS', '-e', 'LOAD_DURATION', '-e', 'LOAD_RATE', '-e', 'LOAD_RUN_ID',
        '-e', 'LOAD_DEVICE_KEY', '-e', 'LOAD_SENSOR_ID',
        $K6Image, 'run', '/work/ingest-load.js'
    )
    $container = & docker @dockerArgs
    if ($LASTEXITCODE -ne 0 -or -not $container) { throw 'Unable to start the k6 container.' }

    while ((& docker inspect --format '{{.State.Running}}' $container) -eq 'true') {
        Add-Sample
        Start-Sleep -Seconds 1
    }
    $k6Exit = & docker wait $container
    & docker logs $container | Set-Content (Join-Path $out 'k6.log')
    if ($k6Exit -ne '0') { throw "k6 exited with code $k6Exit. Inspect the container logs above." }

    $deadline = [DateTime]::UtcNow.AddSeconds($DrainSeconds)
    while ((Get-QueueDepth) -gt 0 -and [DateTime]::UtcNow -lt $deadline) { Add-Sample }
    Add-Sample
    $summary = Get-Content -Raw (Join-Path $out 'k6.json') | ConvertFrom-Json
    $expected = [int]$summary.metrics.queued_unique_events.values.count
    $sql = @"
WITH test_readings AS (
    SELECT e.event_id, r.sensor_id
    FROM device_identifiers i JOIN ingestion_events e ON e.device_id=i.device_id
    JOIN sensor_readings r ON r.ingestion_event_id=e.id WHERE i.public_id='$deviceID'
), duplicates AS (
    SELECT event_id, sensor_id FROM test_readings GROUP BY event_id,sensor_id HAVING COUNT(*)>1
)
SELECT json_build_object(
    'events', (SELECT COUNT(*) FROM ingestion_events e JOIN device_identifiers i ON i.device_id=e.device_id WHERE i.public_id='$deviceID'),
    'readings', (SELECT COUNT(*) FROM test_readings),
    'duplicates', (SELECT COUNT(*) FROM duplicates)
);
"@
    $dbJson = $sql | & docker @compose exec -T postgres sh -c 'psql -X -v ON_ERROR_STOP=1 -tA -U "$POSTGRES_USER" -d "$POSTGRES_DB"'
    if ($LASTEXITCODE -ne 0) { throw 'Unable to verify persisted readings.' }
    $stored = $dbJson | ConvertFrom-Json
    $queueFinal = Get-QueueDepth
    $result = [pscustomobject]@{
        vus = [int]$VUs
        duration = $Duration
        target_events_per_second = $Rate
        dropped_iterations = [int]$summary.metrics.dropped_iterations.values.count
        requests = $summary.metrics.http_reqs.values.count
        requests_per_second = $summary.metrics.http_reqs.values.rate
        p95_ms = $summary.metrics.http_req_duration.values.'p(95)'
        p99_ms = $summary.metrics.http_req_duration.values.'p(99)'
        error_rate = $summary.metrics.http_req_failed.values.rate
        status_503 = $summary.metrics.status_503.values.count
        other_failures = $summary.metrics.other_failures.values.count
        duplicate_attempts = $summary.metrics.duplicate_attempts.values.count
        queued_requests = $summary.metrics.queued_requests.values.count
        accepted_requests_per_second = $summary.metrics.queued_requests.values.rate
        accepted_unique_events_per_second = $summary.metrics.queued_unique_events.values.rate
        confirmed_replays = $summary.metrics.confirmed_replays.values.count
        queued_unique_events = $expected
        persisted_events = $stored.events
        persisted_readings = $stored.readings
        duplicate_readings = $stored.duplicates
        peak_sampled_queue = ($samples | Measure-Object queue_backlog -Maximum).Maximum
        final_queue = $queueFinal
    }
    $result | ConvertTo-Json | Set-Content (Join-Path $out 'result.json')
    $result | Format-List
    Write-Host "Raw data: $out"
    if ($expected -eq 0 -or $queueFinal -ne 0 -or $stored.duplicates -ne 0 -or $stored.events -ne $expected -or $stored.readings -ne $expected) {
        throw 'Persisted counts do not reconcile with acknowledged submissions. Check timeouts and the backlog.'
    }
    if ($Rate -gt 0 -and $result.dropped_iterations -gt 0) { throw 'k6 dropped scheduled iterations. Check generator capacity and latency.' }
} finally {
    ConvertTo-Json -InputObject ($samples.ToArray()) -Depth 8 | Set-Content (Join-Path $out 'samples.json')
    if ($container) { & docker rm -f $container | Out-Null }
    foreach ($variable in $variables) { [Environment]::SetEnvironmentVariable($variable, $previous[$variable], 'Process') }
}

