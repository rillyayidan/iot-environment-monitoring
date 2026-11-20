BEGIN;

LOCK TABLE ingestion_events, sensors IN SHARE ROW EXCLUSIVE MODE;
LOCK TABLE sensor_readings IN ACCESS EXCLUSIVE MODE;

CREATE TABLE sensor_readings_heap (LIKE sensor_readings INCLUDING DEFAULTS);

INSERT INTO sensor_readings_heap (id, ingestion_event_id, sensor_id, value, recorded_at)
SELECT id, ingestion_event_id, sensor_id, value, recorded_at
FROM sensor_readings;

DROP TABLE sensor_readings;
ALTER TABLE sensor_readings_heap RENAME TO sensor_readings;

ALTER TABLE sensor_readings
    ADD PRIMARY KEY (id),
    ADD CONSTRAINT uq_event_sensor UNIQUE (ingestion_event_id, sensor_id),
    ADD CONSTRAINT sensor_readings_ingestion_event_id_fkey FOREIGN KEY (ingestion_event_id)
        REFERENCES ingestion_events (id),
    ADD CONSTRAINT sensor_readings_sensor_id_fkey FOREIGN KEY (sensor_id) REFERENCES sensors (id);

CREATE INDEX idx_sensor_readings_history ON sensor_readings (sensor_id, recorded_at DESC);
ALTER TABLE ingestion_events DROP CONSTRAINT uq_ingestion_event_recorded;

COMMIT;
