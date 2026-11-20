-- Mencocokkan event yang mendapat HTTP 202 dengan data PostgreSQL dan memeriksa duplikat serta alert.
\set ON_ERROR_STOP on
CREATE TEMP TABLE accepted (
    device_id uuid, event_id uuid, recorded_at timestamptz,
    co2 double precision, temperature double precision, humidity double precision,
    PRIMARY KEY (device_id, event_id)
);
SET fleet.ledger_file = :'ledger_file';
DO $copy$
BEGIN
    EXECUTE format('COPY accepted FROM %L WITH (FORMAT csv)', current_setting('fleet.ledger_file'));
END
$copy$;
ANALYZE accepted;

CREATE TEMP TABLE fleet_devices AS
SELECT d.id AS pk, i.public_id AS id, split_part(d.name,'_',4) AS profile
FROM devices d JOIN device_identifiers i ON i.device_id=d.id AND i.retired_at IS NULL
WHERE split_part(d.name,'_',1)='fleet' AND split_part(d.name,'_',2)=:'run_id';
CREATE UNIQUE INDEX ON fleet_devices (pk);
CREATE UNIQUE INDEX ON fleet_devices (id);

CREATE TEMP TABLE fleet_sensors AS
SELECT s.id, s.device_id, s.metric, s.unit, d.profile
FROM sensors s JOIN fleet_devices d ON d.pk=s.device_id;
CREATE UNIQUE INDEX ON fleet_sensors (id);
CREATE UNIQUE INDEX ON fleet_sensors (device_id,metric);

CREATE TEMP TABLE fleet_events AS
SELECT e.id AS pk, d.id AS device_id, e.event_id, e.recorded_at
FROM ingestion_events e JOIN fleet_devices d ON d.pk=e.device_id;
CREATE UNIQUE INDEX ON fleet_events (device_id,event_id);
CREATE UNIQUE INDEX ON fleet_events (pk);

CREATE TEMP TABLE fleet_readings AS
SELECT r.ingestion_event_id AS event_pk,r.sensor_id,r.value,r.recorded_at
FROM sensor_readings r JOIN fleet_events e ON e.pk=r.ingestion_event_id;
CREATE INDEX ON fleet_readings (event_pk,sensor_id);
CREATE INDEX ON fleet_readings (sensor_id,recorded_at);
ANALYZE fleet_devices;
ANALYZE fleet_sensors;
ANALYZE fleet_events;
ANALYZE fleet_readings;

WITH missing AS (
    SELECT count(*) AS n
    FROM accepted a LEFT JOIN fleet_events e
        ON e.device_id=a.device_id AND e.event_id=a.event_id AND e.recorded_at=a.recorded_at
    WHERE e.pk IS NULL
), unexpected AS (
    SELECT count(*) AS n
    FROM fleet_events e LEFT JOIN accepted a ON a.device_id=e.device_id AND a.event_id=e.event_id
    WHERE a.event_id IS NULL
), reading_mismatches AS (
    SELECT count(*) AS n
    FROM accepted a
    JOIN fleet_events e ON e.device_id=a.device_id AND e.event_id=a.event_id
    JOIN fleet_devices d ON d.id=a.device_id
    CROSS JOIN LATERAL (VALUES ('co2', a.co2), ('temperature', a.temperature), ('humidity', a.humidity)) AS wanted(metric,value)
    LEFT JOIN fleet_sensors s ON s.device_id=d.pk AND s.metric=wanted.metric
    LEFT JOIN fleet_readings r ON r.event_pk=e.pk AND r.sensor_id=s.id
    WHERE r.sensor_id IS NULL OR r.value IS DISTINCT FROM wanted.value
       OR r.recorded_at IS DISTINCT FROM a.recorded_at
), reading_duplicates AS (
    SELECT count(*) AS n FROM (
        SELECT event_pk,sensor_id FROM fleet_readings GROUP BY event_pk,sensor_id HAVING count(*)>1
    ) duplicates
), rules AS (
    SELECT r.*,s.profile,s.metric
    FROM alert_rules r JOIN fleet_sensors s ON s.id=r.sensor_id
), lifecycles AS (
    SELECT r.id,r.profile,count(a.id) AS total,
           count(a.id) FILTER (WHERE a.status='ACTIVE') AS active,
           count(a.id) FILTER (WHERE a.status='RESOLVED') AS resolved
    FROM rules r LEFT JOIN alerts a ON a.rule_id=r.id GROUP BY r.id,r.profile
), alert_mismatches AS (
    SELECT count(*) AS n
    FROM lifecycles l
    WHERE CASE l.profile
        WHEN 'normal' THEN l.total<>0
        WHEN 'active' THEN l.total<>1 OR l.active<>1
        WHEN 'recover' THEN l.total<>1 OR l.resolved<>1
        ELSE true
    END
), invalid_alert_readings AS (
    SELECT count(*) AS n
    FROM rules r JOIN alerts a ON a.rule_id=r.id
    WHERE a.severity IS DISTINCT FROM r.severity
       OR a.triggered_value<r.threshold
       OR NOT EXISTS (
           SELECT 1 FROM fleet_readings f WHERE f.sensor_id=r.sensor_id
               AND f.recorded_at=a.triggered_at AND f.value=a.triggered_value
       )
       OR (a.status='ACTIVE' AND a.resolved_at IS NOT NULL)
       OR (a.status='RESOLVED' AND (a.resolved_at IS NULL OR a.resolved_at<=a.triggered_at OR NOT EXISTS (
           SELECT 1 FROM fleet_readings f WHERE f.sensor_id=r.sensor_id
               AND f.recorded_at=a.resolved_at AND f.value<=r.recovery_threshold
       )))
)
SELECT json_build_object(
    'devices', (SELECT count(*) FROM fleet_devices),
    'active_keys', (SELECT count(DISTINCT k.key_hash) FROM device_api_keys k JOIN fleet_devices d ON d.pk=k.device_id WHERE k.revoked_at IS NULL),
    'devices_with_one_key', (SELECT count(*) FROM (SELECT d.pk FROM fleet_devices d LEFT JOIN device_api_keys k ON k.device_id=d.pk AND k.revoked_at IS NULL GROUP BY d.pk HAVING count(k.id)=1) checked),
    'devices_sending', (SELECT count(DISTINCT device_id) FROM accepted),
    'sensors', (SELECT count(*) FROM fleet_sensors),
    'devices_with_three_metrics', (SELECT count(*) FROM (SELECT device_id FROM fleet_sensors GROUP BY device_id HAVING count(*)=3 AND count(DISTINCT metric)=3) checked),
    'rules', (SELECT count(*) FROM rules),
    'sensors_with_one_enabled_rule', (SELECT count(*) FROM (SELECT s.id FROM fleet_sensors s LEFT JOIN rules r ON r.sensor_id=s.id GROUP BY s.id HAVING count(r.id)=1 AND bool_and(r.is_enabled)) checked),
    'accepted_ledger', (SELECT count(*) FROM accepted),
    'persisted_events', (SELECT count(*) FROM fleet_events),
    'persisted_readings', (SELECT count(*) FROM fleet_readings),
    'missing_events', (SELECT n FROM missing),
    'unexpected_events', (SELECT n FROM unexpected),
    'reading_mismatches', (SELECT n FROM reading_mismatches),
    'duplicate_readings', (SELECT n FROM reading_duplicates),
    'expected_active_alerts', (SELECT count(*) FROM rules WHERE profile='active'),
    'expected_resolved_alerts', (SELECT count(*) FROM rules WHERE profile='recover'),
    'active_alerts', (SELECT coalesce(sum(active),0) FROM lifecycles),
    'resolved_alerts', (SELECT coalesce(sum(resolved),0) FROM lifecycles),
    'duplicate_alerts', (SELECT count(*) FROM lifecycles WHERE total>1),
    'alert_state_mismatches', (SELECT n FROM alert_mismatches),
    'invalid_alert_readings', (SELECT n FROM invalid_alert_readings),
    'dead_letters', (SELECT count(*) FROM ingestion_rejections)
);
