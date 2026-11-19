
-- 1. Devices
CREATE TABLE devices (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    name VARCHAR(100) NOT NULL,
    location VARCHAR(200) NOT NULL,
    is_active BOOLEAN NOT NULL DEFAULT TRUE,
    last_seen_at TIMESTAMPTZ,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

-- 2. Device Identifiers
CREATE TABLE device_identifiers (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    device_id UUID NOT NULL REFERENCES devices(id),
    public_id UUID NOT NULL UNIQUE DEFAULT gen_random_uuid(),
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    retired_at TIMESTAMPTZ
);

CREATE UNIQUE INDEX uq_device_active_identifier
    ON device_identifiers (device_id)
    WHERE retired_at IS NULL;

-- 3. Device API Keys
CREATE TABLE device_api_keys (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    device_id UUID NOT NULL REFERENCES devices(id),
    key_hash VARCHAR(64) NOT NULL UNIQUE,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    revoked_at TIMESTAMPTZ
);

-- 4. Sensors
CREATE TABLE sensors (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    device_id UUID NOT NULL REFERENCES devices(id),
    metric VARCHAR(20) NOT NULL,
    unit VARCHAR(10) NOT NULL,
    is_active BOOLEAN NOT NULL DEFAULT TRUE,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),

    CONSTRAINT chk_sensor_metric_unit CHECK (
        (metric = 'co2' AND unit = 'ppm') OR
        (metric = 'temperature' AND unit = 'celsius') OR
        (metric = 'humidity' AND unit = 'percent')
    )
);

-- 5. Sensor Identifiers
CREATE TABLE sensor_identifiers (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    sensor_id UUID NOT NULL REFERENCES sensors(id),
    public_id UUID NOT NULL UNIQUE DEFAULT gen_random_uuid(),
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    retired_at TIMESTAMPTZ
);

CREATE UNIQUE INDEX uq_sensor_active_identifier
    ON sensor_identifiers (sensor_id)
    WHERE retired_at IS NULL;

-- 6. Ingestion Events
CREATE TABLE ingestion_events (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    device_id UUID NOT NULL REFERENCES devices(id),
    event_id UUID NOT NULL,
    payload_hash VARCHAR(64) NOT NULL,
    recorded_at TIMESTAMPTZ NOT NULL,
    received_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),

    CONSTRAINT uq_device_event UNIQUE (device_id, event_id)
);

-- 7. Sensor Readings
CREATE TABLE sensor_readings (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    ingestion_event_id UUID NOT NULL REFERENCES ingestion_events(id),
    sensor_id UUID NOT NULL REFERENCES sensors(id),
    value DOUBLE PRECISION NOT NULL,
    recorded_at TIMESTAMPTZ NOT NULL,

    CONSTRAINT uq_event_sensor UNIQUE (ingestion_event_id, sensor_id)
);

CREATE INDEX idx_sensor_readings_history
    ON sensor_readings (sensor_id, recorded_at DESC);

-- 8. Alert Rules
CREATE TABLE alert_rules (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    sensor_id UUID NOT NULL REFERENCES sensors(id),
    condition VARCHAR(3) NOT NULL,
    threshold DOUBLE PRECISION NOT NULL,
    recovery_threshold DOUBLE PRECISION NOT NULL,
    severity VARCHAR(20) NOT NULL,
    is_enabled BOOLEAN NOT NULL DEFAULT TRUE,

    CONSTRAINT chk_alert_condition
        CHECK (condition IN ('gt', 'gte', 'lt', 'lte')),

    CONSTRAINT chk_alert_severity
        CHECK (severity IN ('warning', 'critical'))
);

-- 9. Alerts
CREATE TABLE alerts (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    rule_id UUID NOT NULL REFERENCES alert_rules(id),
    status VARCHAR(20) NOT NULL DEFAULT 'ACTIVE',
    triggered_value DOUBLE PRECISION NOT NULL,
    triggered_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    resolved_at TIMESTAMPTZ,

    CONSTRAINT chk_alert_status
        CHECK (status IN ('ACTIVE', 'RESOLVED'))
);

CREATE UNIQUE INDEX uq_active_alert_per_rule
    ON alerts (rule_id)
    WHERE status = 'ACTIVE';
