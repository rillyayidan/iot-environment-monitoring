package history

import (
	"context"
	"errors"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/rillyayidan/iot-environment-monitoring/internal/catalog"
)

type Postgres struct {
	db *pgxpool.Pool
}

func NewPostgres(db *pgxpool.Pool) *Postgres { return &Postgres{db: db} }

func (p *Postgres) query(ctx context.Context, filter Filter, query string, read func(pgx.Rows) error, extra ...any) error {
	return pgx.BeginTxFunc(ctx, p.db, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly}, func(tx pgx.Tx) error {
		var device uuid.UUID
		err := tx.QueryRow(ctx, `SELECT device_id FROM device_identifiers WHERE public_id=$1 AND retired_at IS NULL`, filter.Device).Scan(&device)
		if errors.Is(err, pgx.ErrNoRows) {
			return catalog.ErrNotFound
		}
		if err != nil {
			return err
		}
		var sensor *uuid.UUID
		if filter.Sensor != nil {
			err = tx.QueryRow(ctx, `SELECT s.id FROM sensors s JOIN sensor_identifiers i ON s.id=i.sensor_id
				WHERE i.public_id=$1 AND i.retired_at IS NULL AND s.device_id=$2`, filter.Sensor, device).Scan(&sensor)
			if errors.Is(err, pgx.ErrNoRows) {
				return catalog.ErrNotFound
			}
			if err != nil {
				return err
			}
		}
		args := []any{device, sensor, filter.From, filter.To, filter.Limit, filter.Offset}
		rows, err := tx.Query(ctx, query, append(args, extra...)...)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			if err := read(rows); err != nil {
				return err
			}
		}
		return rows.Err()
	})
}

const readingScope = ` FROM sensors s
	JOIN sensor_identifiers i ON i.sensor_id=s.id AND i.retired_at IS NULL
	JOIN sensor_readings r ON r.sensor_id=s.id`

const readingFilter = ` WHERE s.device_id=$1 AND ($2::uuid IS NULL OR s.id=$2)
	AND r.recorded_at >= $3 AND r.recorded_at < $4`

func (p *Postgres) Readings(ctx context.Context, filter Filter) ([]Reading, error) {
	out := make([]Reading, 0)
	err := p.query(ctx, filter, `SELECT e.event_id,i.public_id,s.metric,s.unit,r.value,r.recorded_at`+readingScope+
		` JOIN ingestion_events e ON e.id=r.ingestion_event_id`+readingFilter+
		` ORDER BY r.recorded_at DESC,r.id DESC LIMIT $5 OFFSET $6`, func(rows pgx.Rows) error {
		var r Reading
		if err := rows.Scan(&r.EventID, &r.SensorID, &r.Metric, &r.Unit, &r.Value, &r.RecordedAt); err != nil {
			return err
		}
		r.RecordedAt = r.RecordedAt.UTC()
		out = append(out, r)
		return nil
	})
	return out, err
}

func (p *Postgres) Aggregations(ctx context.Context, filter Filter, width string) ([]Bucket, error) {
	out := make([]Bucket, 0)
	err := p.query(ctx, filter, `SELECT i.public_id,s.metric,s.unit,
		date_bin($7::text::interval,r.recorded_at,TIMESTAMPTZ '2000-01-01T00:00:00Z') AS bucket_start,
		AVG(r.value), MIN(r.value), MAX(r.value), COUNT(*)`+readingScope+readingFilter+
		` GROUP BY i.public_id,s.metric,s.unit,bucket_start
		ORDER BY bucket_start DESC,i.public_id LIMIT $5 OFFSET $6`, func(rows pgx.Rows) error {
		var b Bucket
		if err := rows.Scan(&b.SensorID, &b.Metric, &b.Unit, &b.Start, &b.Average, &b.Min, &b.Max, &b.Count); err != nil {
			return err
		}
		b.Start = b.Start.UTC()
		out = append(out, b)
		return nil
	}, width)
	return out, err
}
