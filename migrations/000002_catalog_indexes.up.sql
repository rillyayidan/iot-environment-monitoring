CREATE INDEX idx_active_device_keys ON device_api_keys (device_id) WHERE revoked_at IS NULL;
CREATE INDEX idx_devices_list ON devices (created_at DESC, id);
CREATE INDEX idx_sensors_device ON sensors (device_id, created_at DESC, id);

