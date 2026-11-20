package monitor

import (
	"context"
	"errors"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/rillyayidan/iot-environment-monitoring/internal/catalog"
)

type Postgres struct {
	db             *pgxpool.Pool
	offlineTimeout time.Duration
}

func NewPostgres(db *pgxpool.Pool, offlineTimeout time.Duration) *Postgres {
	return &Postgres{db: db, offlineTimeout: offlineTimeout}
}

func (p *Postgres) Heartbeat(ctx context.Context, device uuid.UUID) error {
	tag, err := p.db.Exec(ctx, `UPDATE devices SET last_seen_at=GREATEST(last_seen_at,clock_timestamp()),
		connectivity_status='ONLINE' WHERE id=$1 AND is_active`, device)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return catalog.ErrUnauthorized
	}
	return nil
}

// ObserveBatch updates each device once with the latest server receipt time in the transaction.
func (p *Postgres) ObserveBatch(ctx context.Context, tx pgx.Tx, activity map[uuid.UUID]time.Time) error {
	ids := make([]uuid.UUID, 0, len(activity))
	times := make([]time.Time, 0, len(activity))
	for id, at := range activity {
		ids = append(ids, id)
		times = append(times, at)
	}
	_, err := tx.Exec(ctx, `WITH observed AS MATERIALIZED (
		SELECT d.id,GREATEST(d.last_seen_at,LEAST(a.received_at,statement_timestamp())) AS at
		FROM unnest($1::uuid[],$2::timestamptz[]) AS a(id,received_at)
		JOIN devices d ON d.id=a.id
		ORDER BY d.id FOR NO KEY UPDATE OF d
	) UPDATE devices d SET last_seen_at=o.at,connectivity_status=CASE
		WHEN o.at > statement_timestamp() - $3::bigint * interval '1 microsecond' THEN 'ONLINE'
		ELSE 'OFFLINE' END
	FROM observed o WHERE d.id=o.id`, ids, times, p.offlineTimeout.Microseconds())
	return err
}

func (p *Postgres) Status(ctx context.Context, id uuid.UUID) (Status, error) {
	status := Status{ID: id}
	err := p.db.QueryRow(ctx, `SELECT d.is_active,d.last_seen_at,CASE
		WHEN d.last_seen_at IS NULL THEN 'NEVER_SEEN'
		WHEN d.last_seen_at > clock_timestamp() - $2::bigint * interval '1 microsecond' THEN 'ONLINE'
		ELSE 'OFFLINE' END FROM devices d JOIN device_identifiers i ON i.device_id=d.id
		WHERE i.public_id=$1 AND i.retired_at IS NULL`, id, p.offlineTimeout.Microseconds()).
		Scan(&status.Active, &status.LastSeen, &status.Connectivity)
	if errors.Is(err, pgx.ErrNoRows) {
		return Status{}, catalog.ErrNotFound
	}
	if err == nil && status.LastSeen != nil {
		at := status.LastSeen.UTC()
		status.LastSeen = &at
	}
	return status, err
}

func (p *Postgres) MarkOffline(ctx context.Context) (int64, error) {
	tag, err := p.db.Exec(ctx, `WITH due AS (
		SELECT id FROM devices WHERE connectivity_status='ONLINE'
			AND last_seen_at <= statement_timestamp() - $1::bigint * interval '1 microsecond'
		ORDER BY last_seen_at,id LIMIT 256 FOR UPDATE SKIP LOCKED
	) UPDATE devices d SET connectivity_status='OFFLINE' FROM due WHERE d.id=due.id`, p.offlineTimeout.Microseconds())
	return tag.RowsAffected(), err
}
