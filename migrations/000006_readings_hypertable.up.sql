BEGIN;

CREATE EXTENSION IF NOT EXISTS timescaledb WITH SCHEMA public;

-- Lock referenced tables first while migrating existing rows into chunks.
LOCK TABLE ingestion_events, sensors IN SHARE ROW EXCLUSIVE MODE;
LOCK TABLE sensor_readings IN ACCESS EXCLUSIVE MODE;

DO $$
BEGIN
    IF EXISTS (
        SELECT 1
        FROM sensor_readings r
        JOIN ingestion_events e ON e.id = r.ingestion_event_id
        WHERE r.recorded_at IS DISTINCT FROM e.recorded_at
    ) THEN
        RAISE EXCEPTION 'sensor_readings.recorded_at disagrees with its ingestion event';
    END IF;
END
$$;

ALTER TABLE ingestion_events
    ADD CONSTRAINT uq_ingestion_event_recorded UNIQUE (id, recorded_at);

-- Hypertable unique keys include the partition column. The foreign key
-- binds that column to the event so it cannot be changed to bypass deduplication.
ALTER TABLE sensor_readings
    DROP CONSTRAINT sensor_readings_pkey,
    DROP CONSTRAINT uq_event_sensor,
    DROP CONSTRAINT sensor_readings_ingestion_event_id_fkey,
    ADD PRIMARY KEY (id, recorded_at),
    ADD CONSTRAINT uq_event_sensor_time UNIQUE (ingestion_event_id, sensor_id, recorded_at),
    ADD CONSTRAINT fk_reading_event_time FOREIGN KEY (ingestion_event_id, recorded_at)
        REFERENCES ingestion_events (id, recorded_at);

SELECT public.create_hypertable('sensor_readings', public.by_range('recorded_at', INTERVAL '1 day'),
    migrate_data => TRUE, create_default_indexes => FALSE);

COMMIT;
