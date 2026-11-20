DROP INDEX idx_alerts_order;
DROP INDEX idx_alert_rules_sensor;
ALTER TABLE alerts DROP COLUMN severity, DROP COLUMN public_id;
ALTER TABLE alert_rules DROP COLUMN last_evaluated_at, DROP COLUMN public_id;

