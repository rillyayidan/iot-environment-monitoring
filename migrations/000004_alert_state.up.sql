ALTER TABLE alert_rules ADD COLUMN public_id UUID NOT NULL UNIQUE DEFAULT gen_random_uuid();
ALTER TABLE alert_rules ADD COLUMN last_evaluated_at TIMESTAMPTZ;

UPDATE alert_rules a SET last_evaluated_at=GREATEST(
    (SELECT MAX(r.recorded_at) FROM sensor_readings r WHERE r.sensor_id=a.sensor_id),
    (SELECT MAX(GREATEST(x.triggered_at,x.resolved_at)) FROM alerts x WHERE x.rule_id=a.id)
);

ALTER TABLE alerts ADD COLUMN public_id UUID NOT NULL UNIQUE DEFAULT gen_random_uuid();
ALTER TABLE alerts ADD COLUMN severity VARCHAR(20);
UPDATE alerts a SET severity=r.severity FROM alert_rules r WHERE r.id=a.rule_id;
ALTER TABLE alerts ALTER COLUMN severity SET NOT NULL;
ALTER TABLE alerts ADD CONSTRAINT chk_alert_snapshot_severity CHECK (severity IN ('warning','critical'));

CREATE INDEX idx_alert_rules_sensor ON alert_rules (sensor_id, id);
CREATE INDEX idx_alerts_order ON alerts (triggered_at DESC, id);

