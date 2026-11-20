package alert

import (
	"context"
	"errors"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/rillyayidan/iot-environment-monitoring/internal/catalog"
	"github.com/rillyayidan/iot-environment-monitoring/internal/ingest"
)

type scanner interface {
	Scan(...any) error
}

type Postgres struct {
	db *pgxpool.Pool
}

func NewPostgres(db *pgxpool.Pool) *Postgres { return &Postgres{db: db} }

func (p *Postgres) CreateRule(ctx context.Context, sensor uuid.UUID, policy Policy) (Rule, error) {
	rule := Rule{SensorID: sensor, Policy: policy}
	err := p.db.QueryRow(ctx, `INSERT INTO alert_rules (sensor_id, condition, threshold, recovery_threshold, severity, is_enabled)
		SELECT sensor_id,$2,$3,$4,$5,$6 FROM sensor_identifiers WHERE public_id=$1 AND retired_at IS NULL
		RETURNING public_id`, sensor, policy.Operator, policy.Threshold, policy.Recovery, policy.Severity, policy.Enabled).Scan(&rule.ID)
	if errors.Is(err, pgx.ErrNoRows) {
		return Rule{}, catalog.ErrNotFound
	}
	return rule, err
}

func (p *Postgres) ListRules(ctx context.Context, sensor uuid.UUID, limit, offset int) ([]Rule, error) {
	rules := make([]Rule, 0)
	err := pgx.BeginTxFunc(ctx, p.db, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly}, func(tx pgx.Tx) error {
		var internal uuid.UUID
		err := tx.QueryRow(ctx, `SELECT sensor_id FROM sensor_identifiers WHERE public_id=$1 AND retired_at IS NULL`, sensor).Scan(&internal)
		if errors.Is(err, pgx.ErrNoRows) {
			return catalog.ErrNotFound
		}
		if err != nil {
			return err
		}
		rows, err := tx.Query(ctx, `SELECT public_id,condition,threshold,recovery_threshold,severity,is_enabled
			FROM alert_rules WHERE sensor_id=$1 ORDER BY id LIMIT $2 OFFSET $3`, internal, limit, offset)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			rule := Rule{SensorID: sensor}
			if err := rows.Scan(&rule.ID, &rule.Operator, &rule.Threshold, &rule.Recovery, &rule.Severity, &rule.Enabled); err != nil {
				return err
			}
			rules = append(rules, rule)
		}
		return rows.Err()
	})
	return rules, err
}

func (p *Postgres) PatchRule(ctx context.Context, id uuid.UUID, input Input) (Rule, error) {
	rule := Rule{ID: id}
	err := pgx.BeginFunc(ctx, p.db, func(tx pgx.Tx) error {
		var internal uuid.UUID
		err := tx.QueryRow(ctx, `SELECT r.id,i.public_id,r.condition,r.threshold,r.recovery_threshold,r.severity,r.is_enabled
			FROM alert_rules r JOIN sensor_identifiers i ON i.sensor_id=r.sensor_id AND i.retired_at IS NULL
			WHERE r.public_id=$1 FOR UPDATE OF r`, id).
			Scan(&internal, &rule.SensorID, &rule.Operator, &rule.Threshold, &rule.Recovery, &rule.Severity, &rule.Enabled)
		if errors.Is(err, pgx.ErrNoRows) {
			return catalog.ErrNotFound
		}
		if err != nil {
			return err
		}
		rule.Policy, err = input.apply(rule.Policy)
		if err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `UPDATE alert_rules SET condition=$2,threshold=$3,recovery_threshold=$4,severity=$5,is_enabled=$6
			WHERE id=$1`, internal, rule.Operator, rule.Threshold, rule.Recovery, rule.Severity, rule.Enabled); err != nil {
			return err
		}
		if !rule.Enabled {
			_, err := tx.Exec(ctx, `UPDATE alerts SET status='RESOLVED',resolved_at=GREATEST(clock_timestamp(),triggered_at)
				WHERE rule_id=$1 AND status='ACTIVE'`, internal)
			return err
		}
		return nil
	})
	return rule, err
}

const selectAlerts = `SELECT a.public_id,r.public_id,si.public_id,di.public_id,
	a.severity,a.status,a.triggered_value,a.triggered_at,a.resolved_at
	FROM alerts a JOIN alert_rules r ON r.id=a.rule_id
	JOIN sensors s ON s.id=r.sensor_id
	JOIN sensor_identifiers si ON si.sensor_id=s.id AND si.retired_at IS NULL
	JOIN device_identifiers di ON di.device_id=s.device_id AND di.retired_at IS NULL`

func scanAlert(row scanner) (Alert, error) {
	var a Alert
	err := row.Scan(&a.ID, &a.RuleID, &a.SensorID, &a.DeviceID, &a.Severity, &a.Status, &a.Value, &a.TriggeredAt, &a.ResolvedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return Alert{}, catalog.ErrNotFound
	}
	if err != nil {
		return Alert{}, err
	}
	a.TriggeredAt = a.TriggeredAt.UTC()
	if a.ResolvedAt != nil {
		at := a.ResolvedAt.UTC()
		a.ResolvedAt = &at
	}
	return a, nil
}

func (p *Postgres) List(ctx context.Context, status string, limit, offset int) ([]Alert, error) {
	rows, err := p.db.Query(ctx, selectAlerts+` WHERE ($1::text='' OR a.status=$1)
		ORDER BY a.triggered_at DESC,a.id LIMIT $2 OFFSET $3`, status, limit, offset)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	alerts := make([]Alert, 0)
	for rows.Next() {
		a, err := scanAlert(rows)
		if err != nil {
			return nil, err
		}
		alerts = append(alerts, a)
	}
	return alerts, rows.Err()
}

func (p *Postgres) Get(ctx context.Context, id uuid.UUID) (Alert, error) {
	return scanAlert(p.db.QueryRow(ctx, selectAlerts+` WHERE a.public_id=$1`, id))
}

type evaluatedRule struct {
	id, sensor uuid.UUID
	policy     Policy
	watermark  *time.Time
}

type reading struct {
	recorded time.Time
	value    float64
}

func (p *Postgres) Evaluate(ctx context.Context, tx pgx.Tx, sensor uuid.UUID, value float64, recorded time.Time) error {
	return evaluate(ctx, tx, map[uuid.UUID][]reading{sensor: {{recorded.Truncate(time.Microsecond), value}}})
}

// EvaluateBatch locks rules in a consistent order and advances each watermark once per transaction.
func (p *Postgres) EvaluateBatch(ctx context.Context, tx pgx.Tx, events []ingest.Event) error {
	bySensor := make(map[uuid.UUID][]reading)
	for _, event := range events {
		recorded := event.RecordedAt.Truncate(time.Microsecond)
		for _, m := range event.Measurements {
			bySensor[m.SensorID] = append(bySensor[m.SensorID], reading{recorded, m.Value})
		}
	}
	return evaluate(ctx, tx, bySensor)
}

func evaluate(ctx context.Context, tx pgx.Tx, bySensor map[uuid.UUID][]reading) error {
	ids := make([]uuid.UUID, 0, len(bySensor))
	for id := range bySensor {
		ids = append(ids, id)
	}
	rows, err := tx.Query(ctx, `SELECT id,sensor_id,condition,threshold,recovery_threshold,severity,last_evaluated_at
		FROM alert_rules WHERE sensor_id=ANY($1) AND is_enabled ORDER BY sensor_id,id FOR UPDATE`, ids)
	if err != nil {
		return err
	}
	rules, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (evaluatedRule, error) {
		var r evaluatedRule
		err := row.Scan(&r.id, &r.sensor, &r.policy.Operator, &r.policy.Threshold, &r.policy.Recovery, &r.policy.Severity, &r.watermark)
		return r, err
	})
	if err != nil {
		return err
	}
	for _, rule := range rules {
		if err := applyReadings(ctx, tx, rule, bySensor[rule.sensor]); err != nil {
			return err
		}
	}
	return nil
}

func applyReadings(ctx context.Context, tx pgx.Tx, rule evaluatedRule, readings []reading) error {
	var latest time.Time
	if rule.watermark != nil {
		latest = *rule.watermark
	}
	var active, known, advanced bool
	for _, item := range readings {
		if !item.recorded.After(latest) {
			continue
		}
		latest, advanced = item.recorded, true
		trigger, recover := rule.policy.Triggers(item.value), rule.policy.Recovers(item.value)
		if !trigger && !recover {
			continue
		}
		if !known {
			if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM alerts WHERE rule_id=$1 AND status='ACTIVE')`, rule.id).Scan(&active); err != nil {
				return err
			}
			known = true
		}
		switch {
		case trigger && !active:
			_, err := tx.Exec(ctx, `INSERT INTO alerts (rule_id,triggered_value,triggered_at,severity)
				VALUES ($1,$2,$3,$4) ON CONFLICT (rule_id) WHERE status='ACTIVE' DO NOTHING`,
				rule.id, item.value, item.recorded, rule.policy.Severity)
			if err != nil {
				return err
			}
			active = true
		case recover && !trigger && active:
			_, err := tx.Exec(ctx, `UPDATE alerts SET status='RESOLVED',resolved_at=$2
				WHERE rule_id=$1 AND status='ACTIVE'`, rule.id, item.recorded)
			if err != nil {
				return err
			}
			active = false
		}
	}
	if advanced {
		_, err := tx.Exec(ctx, `UPDATE alert_rules SET last_evaluated_at=$2 WHERE id=$1`, rule.id, latest)
		return err
	}
	return nil
}
