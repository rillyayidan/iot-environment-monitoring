package catalog

import (
	"context"
	"crypto/subtle"
	"errors"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type Postgres struct {
	db *pgxpool.Pool
}

func NewPostgres(db *pgxpool.Pool) *Postgres {
	return &Postgres{db: db}
}

type scanner interface {
	Scan(...any) error
}

func scanDevice(row scanner) (Device, error) {
	var d Device
	err := row.Scan(&d.ID, &d.Name, &d.Location, &d.Active, &d.LastSeen, &d.Created)
	if errors.Is(err, pgx.ErrNoRows) {
		return Device{}, ErrNotFound
	}
	if err != nil {
		return Device{}, err
	}
	d.Created = d.Created.UTC()
	if d.LastSeen != nil {
		lastSeen := d.LastSeen.UTC()
		d.LastSeen = &lastSeen
	}
	return d, nil
}

const selectDevice = `SELECT i.public_id, d.name, d.location, d.is_active, d.last_seen_at, d.created_at
	FROM devices d JOIN device_identifiers i ON i.device_id = d.id AND i.retired_at IS NULL`

func (p *Postgres) CreateDevice(ctx context.Context, input DeviceInput, hash string) (Device, error) {
	var device Device
	err := pgx.BeginFunc(ctx, p.db, func(tx pgx.Tx) error {
		var internal uuid.UUID
		err := tx.QueryRow(ctx, `INSERT INTO devices (name,location,is_active)
			VALUES ($1,$2,COALESCE($3, true)) RETURNING id`, input.Name, input.Location, input.Active).Scan(&internal)
		if err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `INSERT INTO device_identifiers (device_id) VALUES ($1)`, internal); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `INSERT INTO device_api_keys (device_id,key_hash) VALUES ($1,$2)`, internal, hash); err != nil {
			return err
		}
		device, err = scanDevice(tx.QueryRow(ctx, selectDevice+` WHERE d.id=$1`, internal))
		return err
	})
	return device, err
}

func (p *Postgres) ListDevices(ctx context.Context, limit, offset int) ([]Device, error) {
	rows, err := p.db.Query(ctx, selectDevice+` ORDER BY d.created_at DESC,d.id LIMIT $1 OFFSET $2`, limit, offset)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	devices := make([]Device, 0)
	for rows.Next() {
		device, err := scanDevice(rows)
		if err != nil {
			return nil, err
		}
		devices = append(devices, device)
	}
	return devices, rows.Err()
}

func (p *Postgres) Device(ctx context.Context, id uuid.UUID) (Device, error) {
	return scanDevice(p.db.QueryRow(ctx, selectDevice+` WHERE i.public_id=$1`, id))
}

func lockDevice(ctx context.Context, tx pgx.Tx, id uuid.UUID) (uuid.UUID, error) {
	var internal uuid.UUID
	err := tx.QueryRow(ctx, `SELECT d.id FROM devices d JOIN device_identifiers i ON i.device_id=d.id
		WHERE i.public_id=$1 AND i.retired_at IS NULL FOR NO KEY UPDATE OF d,i`, id).Scan(&internal)
	if errors.Is(err, pgx.ErrNoRows) {
		return uuid.Nil, ErrNotFound
	}
	return internal, err
}

func (p *Postgres) changeDevice(ctx context.Context, id uuid.UUID, change func(pgx.Tx, uuid.UUID) error) (Device, error) {
	var device Device
	err := pgx.BeginFunc(ctx, p.db, func(tx pgx.Tx) error {
		internal, err := lockDevice(ctx, tx, id)
		if err != nil {
			return err
		}
		if err := change(tx, internal); err != nil {
			return err
		}
		device, err = scanDevice(tx.QueryRow(ctx, selectDevice+` WHERE d.id=$1`, internal))
		return err
	})
	return device, err
}

func (p *Postgres) PatchDevice(ctx context.Context, id uuid.UUID, input DeviceInput) (Device, error) {
	return p.changeDevice(ctx, id, func(tx pgx.Tx, internal uuid.UUID) error {
		_, err := tx.Exec(ctx, `UPDATE devices SET name=COALESCE($2,name),
			location=COALESCE($3,location), is_active=COALESCE($4,is_active) WHERE id=$1`,
			internal, input.Name, input.Location, input.Active)
		return err
	})
}

func (p *Postgres) RotateDeviceID(ctx context.Context, id uuid.UUID) (Device, error) {
	return p.changeDevice(ctx, id, func(tx pgx.Tx, internal uuid.UUID) error {
		if _, err := tx.Exec(ctx, `UPDATE device_identifiers SET retired_at=clock_timestamp()
			WHERE device_id=$1 AND retired_at IS NULL`, internal); err != nil {
			return err
		}
		_, err := tx.Exec(ctx, `INSERT INTO device_identifiers(device_id, created_at) VALUES ($1, clock_timestamp())`, internal)
		return err
	})
}

func (p *Postgres) RotateKey(ctx context.Context, id uuid.UUID, hash string) (Device, error) {
	return p.changeDevice(ctx, id, func(tx pgx.Tx, internal uuid.UUID) error {
		if _, err := tx.Exec(ctx, `UPDATE device_api_keys SET revoked_at=clock_timestamp()
			WHERE device_id=$1 AND revoked_at IS NULL`, internal); err != nil {
			return err
		}
		_, err := tx.Exec(ctx, `INSERT INTO device_api_keys(device_id,key_hash,created_at) VALUES ($1,$2,clock_timestamp())`, internal, hash)
		return err
	})
}

func (p *Postgres) Authenticate(ctx context.Context, hash string) (uuid.UUID, error) {
	var id uuid.UUID
	var stored string
	err := p.db.QueryRow(ctx, `SELECT d.id,k.key_hash FROM device_api_keys k JOIN devices d ON d.id=k.device_id
		WHERE k.key_hash=$1 AND k.revoked_at IS NULL AND d.is_active`, hash).Scan(&id, &stored)
	if errors.Is(err, pgx.ErrNoRows) {
		return uuid.Nil, ErrUnauthorized
	}
	if err != nil {
		return uuid.Nil, err
	}
	if subtle.ConstantTimeCompare([]byte(hash), []byte(stored)) != 1 {
		return uuid.Nil, ErrUnauthorized
	}
	return id, nil
}

func scanSensor(row scanner) (Sensor, error) {
	var s Sensor
	err := row.Scan(&s.ID, &s.DeviceID, &s.Metric, &s.Unit, &s.Active, &s.Created)
	if errors.Is(err, pgx.ErrNoRows) {
		return Sensor{}, ErrNotFound
	}
	if err == nil {
		s.Created = s.Created.UTC()
	}
	return s, err
}

const selectSensor = `SELECT i.public_id,d.public_id,s.metric,s.unit,s.is_active,s.created_at
	FROM sensors s JOIN sensor_identifiers i ON i.sensor_id=s.id AND i.retired_at IS NULL
	JOIN device_identifiers d ON d.device_id=s.device_id AND d.retired_at IS NULL`

func (p *Postgres) CreateSensor(ctx context.Context, device uuid.UUID, input SensorInput, unit string) (Sensor, error) {
	var sensor Sensor
	err := pgx.BeginFunc(ctx, p.db, func(tx pgx.Tx) error {
		internalDevice, err := lockDevice(ctx, tx, device)
		if err != nil {
			return err
		}
		var internal uuid.UUID
		err = tx.QueryRow(ctx, `INSERT INTO sensors (device_id,metric,unit,is_active)
			VALUES ($1,$2,$3,COALESCE($4,true)) RETURNING id`,
			internalDevice, input.Metric, unit, input.Active).Scan(&internal)
		if err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `INSERT INTO sensor_identifiers (sensor_id) VALUES ($1)`, internal); err != nil {
			return err
		}
		sensor, err = scanSensor(tx.QueryRow(ctx, selectSensor+` WHERE s.id=$1`, internal))
		return err
	})
	return sensor, err
}

func (p *Postgres) ListSensors(ctx context.Context, device uuid.UUID, limit, offset int) ([]Sensor, error) {
	sensors := make([]Sensor, 0)
	err := pgx.BeginTxFunc(ctx, p.db, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly}, func(tx pgx.Tx) error {
		if _, err := scanDevice(tx.QueryRow(ctx, selectDevice+` WHERE i.public_id=$1`, device)); err != nil {
			return err
		}
		rows, err := tx.Query(ctx, selectSensor+` WHERE d.public_id=$1 ORDER BY s.created_at DESC,s.id LIMIT $2 OFFSET $3`, device, limit, offset)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			sensor, err := scanSensor(rows)
			if err != nil {
				return err
			}
			sensors = append(sensors, sensor)
		}
		return rows.Err()
	})
	return sensors, err
}

func (p *Postgres) Sensor(ctx context.Context, id uuid.UUID) (Sensor, error) {
	return scanSensor(p.db.QueryRow(ctx, selectSensor+` WHERE i.public_id=$1`, id))
}

func (p *Postgres) changeSensor(ctx context.Context, id uuid.UUID, change func(pgx.Tx, uuid.UUID) error) (Sensor, error) {
	var sensor Sensor
	err := pgx.BeginFunc(ctx, p.db, func(tx pgx.Tx) error {
		var internal uuid.UUID
		err := tx.QueryRow(ctx, `SELECT s.id FROM sensors s JOIN sensor_identifiers i ON i.sensor_id=s.id
			WHERE i.public_id=$1 AND i.retired_at IS NULL FOR NO KEY UPDATE OF s,i`, id).Scan(&internal)
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrNotFound
		}
		if err != nil {
			return err
		}
		if err := change(tx, internal); err != nil {
			return err
		}
		sensor, err = scanSensor(tx.QueryRow(ctx, selectSensor+` WHERE s.id=$1`, internal))
		return err
	})
	return sensor, err
}

func (p *Postgres) PatchSensor(ctx context.Context, id uuid.UUID, active bool) (Sensor, error) {
	return p.changeSensor(ctx, id, func(tx pgx.Tx, internal uuid.UUID) error {
		_, err := tx.Exec(ctx, `UPDATE sensors SET is_active=$2 WHERE id=$1`, internal, active)
		return err
	})
}

func (p *Postgres) RotateSensorID(ctx context.Context, id uuid.UUID) (Sensor, error) {
	return p.changeSensor(ctx, id, func(tx pgx.Tx, internal uuid.UUID) error {
		if _, err := tx.Exec(ctx, `UPDATE sensor_identifiers SET retired_at=clock_timestamp()
			WHERE sensor_id=$1 AND retired_at IS NULL`, internal); err != nil {
			return err
		}
		_, err := tx.Exec(ctx, `INSERT INTO sensor_identifiers(sensor_id,created_at) VALUES ($1,clock_timestamp())`, internal)
		return err
	})
}
