ALTER TABLE devices ADD COLUMN connectivity_status VARCHAR(10) NOT NULL DEFAULT 'NEVER_SEEN';
ALTER TABLE devices ADD CONSTRAINT chk_device_connectivity
    CHECK (connectivity_status IN ('NEVER_SEEN','ONLINE','OFFLINE'));

-- The monitor reconciles this baseline using the configured timeout.
UPDATE devices SET connectivity_status='ONLINE' WHERE last_seen_at IS NOT NULL;
CREATE INDEX idx_online_device_sweep ON devices (last_seen_at,id) WHERE connectivity_status='ONLINE';

