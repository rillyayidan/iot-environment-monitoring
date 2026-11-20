package ingest

import (
	"context"
	"encoding/json"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type Evaluator interface {
	Evaluate(context.Context, pgx.Tx, uuid.UUID, float64, time.Time) error
}

type batchEvaluator interface {
	EvaluateBatch(context.Context, pgx.Tx, []Event) error
}

type Activity interface {
	ObserveBatch(context.Context, pgx.Tx, map[uuid.UUID]time.Time) error
}

type Postgres struct {
	db       *pgxpool.Pool
	alerts   Evaluator
	activity Activity
}

func NewPostgres(db *pgxpool.Pool, alerts Evaluator, activity Activity) *Postgres {
	return &Postgres{db: db, alerts: alerts, activity: activity}
}

func (p *Postgres) OwnedSensors(ctx context.Context, device uuid.UUID, ids []uuid.UUID) (map[uuid.UUID]OwnedSensor, error) {
	rows, err := p.db.Query(ctx, `SELECT i.public_id, s.id, s.metric
		FROM sensor_identifiers i JOIN sensors s ON s.id=i.sensor_id
		WHERE i.public_id=ANY($1) AND i.retired_at IS NULL AND s.device_id=$2 AND s.is_active`, ids, device)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := make(map[uuid.UUID]OwnedSensor, len(ids))
	for rows.Next() {
		var public uuid.UUID
		var sensor OwnedSensor
		if err := rows.Scan(&public, &sensor.InternalID, &sensor.Metric); err != nil {
			return nil, err
		}
		result[public] = sensor
	}
	return result, rows.Err()
}

// Persist returns only after the transaction commits. An identical replay is a successful no-op.
func (p *Postgres) Persist(ctx context.Context, event Event) error {
	return p.PersistBatch(ctx, []Event{event})
}

type eventKey struct {
	device, event uuid.UUID
}

type eventRow struct {
	DeviceID   uuid.UUID `json:"device_id"`
	EventID    uuid.UUID `json:"event_id"`
	Hash       string    `json:"payload_hash"`
	RecordedAt time.Time `json:"recorded_at"`
	ReceivedAt time.Time `json:"received_at"`
}

type readingRow struct {
	EventPK    uuid.UUID `json:"event_pk"`
	DeviceID   uuid.UUID `json:"device_id"`
	RecordedAt time.Time `json:"recorded_at"`
	Measurement
}

// PersistBatch commits the entire batch or rolls it back. A worker can isolate a
// domain rejection by retrying individual entries after the rollback.
func (p *Postgres) PersistBatch(ctx context.Context, events []Event) error {
	if len(events) == 0 {
		return nil
	}
	unique := make([]Event, 0, len(events))
	rows := make([]eventRow, 0, len(events))
	hashes := make(map[eventKey]string, len(events))
	for _, event := range events {
		if err := event.Validate(); err != nil {
			return err
		}
		key := eventKey{event.DeviceID, event.EventID}
		if previous, exists := hashes[key]; exists {
			if previous != event.Hash {
				return ErrConflict
			}
			continue
		}
		hashes[key] = event.Hash
		unique = append(unique, event)
		rows = append(rows, eventRow{event.DeviceID, event.EventID, event.Hash,
			event.RecordedAt.Truncate(time.Microsecond), event.ReceivedAt.Truncate(time.Microsecond)})
	}
	payload, err := json.Marshal(rows)
	if err != nil {
		return err
	}

	return pgx.BeginTxFunc(ctx, p.db, pgx.TxOptions{IsoLevel: pgx.ReadCommitted}, func(tx pgx.Tx) error {
		if err := lockForNewReadingChunks(ctx, tx, payload); err != nil {
			return err
		}
		inserted, err := insertEvents(ctx, tx, payload)
		if err != nil {
			return err
		}
		if len(inserted) < len(unique) {
			// A separate statement reads the winner after ON CONFLICT has waited.
			var matches int
			err := tx.QueryRow(ctx, `SELECT count(*) FROM
				jsonb_to_recordset($1::jsonb) AS incoming(device_id uuid,event_id uuid,payload_hash text)
				JOIN ingestion_events e USING (device_id,event_id)
				WHERE e.payload_hash=incoming.payload_hash`, payload).Scan(&matches)
			if err != nil {
				return err
			}
			if matches != len(unique) {
				return ErrConflict
			}
		}
		if len(inserted) == 0 {
			return nil
		}

		newEvents := make([]Event, 0, len(inserted))
		readings := make([]readingRow, 0, len(inserted))
		activity := make(map[uuid.UUID]time.Time)
		for _, event := range unique {
			id, exists := inserted[eventKey{event.DeviceID, event.EventID}]
			if !exists {
				continue
			}
			newEvents = append(newEvents, event)
			for _, m := range event.Measurements {
				readings = append(readings, readingRow{id, event.DeviceID, event.RecordedAt.Truncate(time.Microsecond), m})
			}
			if event.ReceivedAt.After(activity[event.DeviceID]) {
				activity[event.DeviceID] = event.ReceivedAt.Truncate(time.Microsecond)
			}
		}

		if err := insertReadings(ctx, tx, readings); err != nil {
			return err
		}
		if err := p.evaluateAlerts(ctx, tx, newEvents); err != nil {
			return err
		}
		if p.activity != nil {
			return p.activity.ObserveBatch(ctx, tx, activity)
		}
		return nil
	})
}

func lockForNewReadingChunks(ctx context.Context, tx pgx.Tx, payload []byte) error {
	var needsChunk bool
	err := tx.QueryRow(ctx, `SELECT EXISTS (
		SELECT 1 FROM jsonb_to_recordset($1::jsonb) AS incoming(recorded_at timestamptz)
		WHERE NOT EXISTS (
			SELECT 1 FROM timescaledb_information.chunks c
			WHERE c.hypertable_schema=current_schema() AND c.hypertable_name='sensor_readings'
				AND incoming.recorded_at >= c.range_start AND incoming.recorded_at < c.range_end
		)
	)`, payload).Scan(&needsChunk)
	if err != nil || !needsChunk {
		return err
	}
	// A new chunk's foreign keys lock the referenced tables. Take those locks
	// before event inserts can block on another worker's unique key.
	_, err = tx.Exec(ctx, `LOCK TABLE ingestion_events, sensors IN SHARE ROW EXCLUSIVE MODE`)
	return err
}

func insertEvents(ctx context.Context, tx pgx.Tx, payload []byte) (map[eventKey]uuid.UUID, error) {
	rows, err := tx.Query(ctx, `INSERT INTO ingestion_events (device_id,event_id,payload_hash,recorded_at,received_at)
		SELECT device_id,event_id,payload_hash,recorded_at,received_at
		FROM jsonb_to_recordset($1::jsonb) AS incoming(device_id uuid,event_id uuid,payload_hash text,
			recorded_at timestamptz,received_at timestamptz)
		ORDER BY device_id,event_id
		ON CONFLICT (device_id,event_id) DO NOTHING RETURNING device_id,event_id,id`, payload)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	inserted := make(map[eventKey]uuid.UUID)
	for rows.Next() {
		var key eventKey
		var id uuid.UUID
		if err := rows.Scan(&key.device, &key.event, &id); err != nil {
			return nil, err
		}
		inserted[key] = id
	}
	return inserted, rows.Err()
}

func insertReadings(ctx context.Context, tx pgx.Tx, readings []readingRow) error {
	payload, err := json.Marshal(readings)
	if err != nil {
		return err
	}
	tag, err := tx.Exec(ctx, `INSERT INTO sensor_readings (ingestion_event_id,sensor_id,value,recorded_at)
		SELECT m.event_pk,s.id,m.value,m.recorded_at
		FROM jsonb_to_recordset($1::jsonb) AS m(event_pk uuid,device_id uuid,sensor_id uuid,
			metric text,value double precision,recorded_at timestamptz)
		JOIN sensors s ON s.id=m.sensor_id AND s.device_id=m.device_id AND s.metric=m.metric`, payload)
	if err != nil {
		return err
	}
	if tag.RowsAffected() != int64(len(readings)) {
		return ErrInvalidEvent
	}
	return nil
}

func (p *Postgres) evaluateAlerts(ctx context.Context, tx pgx.Tx, events []Event) error {
	if evaluator, ok := p.alerts.(batchEvaluator); ok {
		return evaluator.EvaluateBatch(ctx, tx, events)
	}
	if p.alerts != nil {
		for _, event := range events {
			for _, m := range event.Measurements {
				if err := p.alerts.Evaluate(ctx, tx, m.SensorID, m.Value, event.RecordedAt); err != nil {
					return err
				}
			}
		}
	}
	return nil
}

// Reject commits the dead-letter record before the consumer may remove the stream entry.
func (p *Postgres) Reject(ctx context.Context, source, id, reason, payload string) error {
	_, err := p.db.Exec(ctx, `INSERT INTO ingestion_rejections(stream, message_id, reason, payload)
		VALUES ($1,$2,$3,NULLIF($4,'')) ON CONFLICT (stream,message_id) DO NOTHING`, source, id, reason, payload)
	return err
}
