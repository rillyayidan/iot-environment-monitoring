package integration

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/redis/go-redis/v9"
	"github.com/rillyayidan/iot-environment-monitoring/internal/alert"
	"github.com/rillyayidan/iot-environment-monitoring/internal/catalog"
	"github.com/rillyayidan/iot-environment-monitoring/internal/history"
	"github.com/rillyayidan/iot-environment-monitoring/internal/ingest"
	"github.com/rillyayidan/iot-environment-monitoring/internal/monitor"
	"github.com/rillyayidan/iot-environment-monitoring/internal/router"
	"github.com/rillyayidan/iot-environment-monitoring/internal/stream"
)

const adminToken = "integration-test-admin"

type fixture struct {
	db      *pgxpool.Pool
	client  *redis.Client
	queue   *stream.Queue
	store   *ingest.Postgres
	monitor *monitor.Postgres
	router  http.Handler
}

func check(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

func setup(t *testing.T, queueLimit int) *fixture {
	t.Helper()
	return setupWithLegacyData(t, queueLimit, nil)
}

func setupWithLegacyData(t *testing.T, queueLimit int, seed func(*pgxpool.Pool)) *fixture {
	t.Helper()
	dsn, redisURL := os.Getenv("TEST_DATABASE_URL"), os.Getenv("TEST_REDIS_URL")
	if dsn == "" && redisURL == "" {
		t.Skip("set TEST_DATABASE_URL and TEST_REDIS_URL for PostgreSQL/Redis integration")
	}
	if dsn == "" || redisURL == "" {
		t.Fatal("both TEST_DATABASE_URL and TEST_REDIS_URL are required")
	}
	ctx := t.Context()
	root, err := pgxpool.New(ctx, dsn)
	check(t, err)
	t.Cleanup(root.Close)
	schema := "iot_test_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	quoted := pgx.Identifier{schema}.Sanitize()
	_, err = root.Exec(ctx, "CREATE SCHEMA "+quoted)
	check(t, err)
	t.Cleanup(func() {
		_, err := root.Exec(context.Background(), "DROP SCHEMA "+quoted+" CASCADE")
		check(t, err)
	})

	cfg, err := pgxpool.ParseConfig(dsn)
	check(t, err)
	cfg.ConnConfig.RuntimeParams["search_path"] = quoted
	db, err := pgxpool.NewWithConfig(ctx, cfg)
	check(t, err)
	t.Cleanup(db.Close)
	files, err := filepath.Glob("../migrations/*.up.sql")
	check(t, err)
	if len(files) == 0 {
		t.Fatal("no migrations found")
	}
	for i, path := range files {
		sql, err := os.ReadFile(path)
		check(t, err)
		_, err = db.Exec(ctx, string(sql))
		if err != nil {
			t.Fatalf("%s: %v", path, err)
		}
		if i == 0 && seed != nil {
			seed(db)
		}
	}

	client, err := stream.Open(redisURL)
	check(t, err)
	t.Cleanup(func() { check(t, client.Close()) })
	check(t, client.Ping(ctx).Err())
	name := "iot:test:" + uuid.NewString()
	t.Cleanup(func() { check(t, client.Del(context.Background(), name).Err()) })
	queue := stream.New(client, name, "workers", queueLimit)
	monitoring := monitor.NewPostgres(db, time.Minute)
	return &fixture{
		db: db, client: client, queue: queue, monitor: monitoring,
		store: ingest.NewPostgres(db, alert.NewPostgres(db), monitoring),
		router: router.NewWithOptions(db, router.Options{
			AdminToken: adminToken, Producer: queue, OfflineTimeout: time.Minute,
			RedisPing: func(ctx context.Context) error { return client.Ping(ctx).Err() },
		}),
	}
}

func (f *fixture) request(t *testing.T, method, path, auth, key string, input, output any, status int) *httptest.ResponseRecorder {
	t.Helper()
	data, err := json.Marshal(input)
	check(t, err)
	req := httptest.NewRequest(method, path, bytes.NewReader(data))
	req.Header.Set("Content-Type", "application/json")
	if auth != "" {
		req.Header.Set("Authorization", "Bearer "+auth)
	}
	if key != "" {
		req.Header.Set("X-API-Key", key)
	}
	rec := httptest.NewRecorder()
	f.router.ServeHTTP(rec, req)
	if rec.Code != status {
		t.Fatalf("%s %s: got HTTP %d, want %d", method, path, rec.Code, status)
	}
	if output != nil {
		check(t, json.Unmarshal(rec.Body.Bytes(), output))
	}
	return rec
}

func (f *fixture) admin(t *testing.T, method, path string, input, output any, status int) *httptest.ResponseRecorder {
	t.Helper()
	return f.request(t, method, "/api/v1"+path, adminToken, "", input, output, status)
}

func (f *fixture) device(t *testing.T) catalog.DeviceCredential {
	t.Helper()
	var d catalog.DeviceCredential
	rec := f.admin(t, http.MethodPost, "/devices", map[string]any{"name": "lab", "location": "room"}, &d, 201)
	if d.ID == uuid.Nil || len(d.APIKey) != 43 || rec.Header().Get("Cache-Control") != "no-store" {
		t.Fatal("invalid registration response")
	}
	return d
}

func (f *fixture) sensor(t *testing.T, device uuid.UUID, metric string) catalog.Sensor {
	t.Helper()
	var s catalog.Sensor
	f.admin(t, http.MethodPost, "/devices/"+device.String()+"/sensors", map[string]string{"metric": metric}, &s, 201)
	unit, _ := catalog.Unit(metric)
	if s.ID == uuid.Nil || s.DeviceID != device || s.Unit != unit || !s.Active {
		t.Fatal("invalid sensor response")
	}
	return s
}

func (f *fixture) startWorker(t *testing.T, processor stream.Processor, parallel int) {
	t.Helper()
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan struct{})
	go func() {
		defer close(done)
		stream.NewWorker(f.queue, processor, parallel, 32, 200*time.Millisecond, 2*time.Second).Run(ctx)
	}()
	t.Cleanup(func() {
		cancel()
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Error("worker did not stop")
		}
	})
}

func eventually(t *testing.T, condition func() bool) {
	t.Helper()
	deadline := time.Now().Add(8 * time.Second)
	for time.Now().Before(deadline) {
		if condition() {
			return
		}
		time.Sleep(30 * time.Millisecond)
	}
	t.Fatal("condition did not become true")
}

func (f *fixture) drain(t *testing.T) {
	t.Helper()
	eventually(t, func() bool {
		n, err := f.client.XLen(t.Context(), f.queue.Name()).Result()
		check(t, err)
		return n == 0
	})
}

func count(t *testing.T, db *pgxpool.Pool, sql string, args ...any) int {
	t.Helper()
	var n int
	check(t, db.QueryRow(t.Context(), sql, args...).Scan(&n))
	return n
}

func reading(id, sensor uuid.UUID, at time.Time, value float64) ingest.Input {
	return ingest.Input{EventID: id, RecordedAt: at, Measurements: []ingest.MeasurementInput{
		{SensorID: sensor, Metric: "co2", Value: &value},
	}}
}

func (f *fixture) submit(t *testing.T, key string, input ingest.Input, status int) {
	t.Helper()
	f.request(t, http.MethodPost, "/api/v1/readings", "", key, input, nil, status)
}

func TestCatalogAndAuthentication(t *testing.T) {
	f := setup(t, 10)
	f.request(t, http.MethodGet, "/health/live", "", "", nil, nil, 200)
	f.request(t, http.MethodGet, "/health/ready", "", "", nil, nil, 200)
	f.request(t, http.MethodGet, "/api/v1/devices", "", "", nil, nil, 401)
	f.request(t, http.MethodGet, "/api/v1/devices", "incorrect", "", nil, nil, 401)
	d := f.device(t)
	oldDevice := "/devices/" + d.ID.String()
	rec := f.admin(t, http.MethodGet, oldDevice, nil, nil, 200)
	if strings.Contains(rec.Body.String(), "api_key") || strings.Contains(rec.Body.String(), d.APIKey) {
		t.Fatal("device GET exposed credentials")
	}
	var devices []catalog.Device
	f.admin(t, http.MethodGet, "/devices?limit=1", nil, &devices, 200)
	if len(devices) != 1 || devices[0].ID != d.ID {
		t.Fatal("registered device missing from collection")
	}

	var internal uuid.UUID
	var hash string
	check(t, f.db.QueryRow(t.Context(), `SELECT i.device_id,k.key_hash FROM device_identifiers i
		JOIN device_api_keys k ON k.device_id=i.device_id WHERE i.public_id=$1`, d.ID).Scan(&internal, &hash))
	if internal == d.ID || hash == d.APIKey || len(hash) != 64 {
		t.Fatal("credential storage or identity separation is incorrect")
	}

	s := f.sensor(t, d.ID, "co2")
	f.sensor(t, d.ID, "co2")
	f.admin(t, http.MethodPost, oldDevice+"/sensors", map[string]string{"metric": "oxygen"}, nil, 400)
	var sensors []catalog.Sensor
	f.admin(t, http.MethodGet, oldDevice+"/sensors", nil, &sensors, 200)
	if len(sensors) != 2 {
		t.Fatal("multiple sensors of the same metric were not returned")
	}
	oldSensor := "/sensors/" + s.ID.String()
	var rotatedSensor catalog.Sensor
	f.admin(t, http.MethodPost, oldSensor+"/identifiers/rotate", nil, &rotatedSensor, 200)
	f.admin(t, http.MethodGet, oldSensor, nil, nil, 404)
	f.admin(t, http.MethodPatch, "/sensors/"+rotatedSensor.ID.String(), map[string]bool{"is_active": false}, nil, 200)
	f.submit(t, d.APIKey, reading(uuid.New(), rotatedSensor.ID, time.Now().UTC(), 700), 403)
	if count(t, f.db, `SELECT count(*) FROM sensor_identifiers a JOIN sensor_identifiers b USING (sensor_id)
		WHERE a.public_id=$1 AND a.retired_at IS NOT NULL AND b.public_id=$2 AND b.retired_at IS NULL`, s.ID, rotatedSensor.ID) != 1 {
		t.Fatal("sensor rotation lost identifier history")
	}

	var rotatedID, renamed catalog.Device
	f.admin(t, http.MethodPost, oldDevice+"/identifiers/rotate", nil, &rotatedID, 200)
	f.admin(t, http.MethodGet, oldDevice, nil, nil, 404)
	if count(t, f.db, `SELECT count(*) FROM device_identifiers a JOIN device_identifiers b USING (device_id)
		WHERE a.public_id=$1 AND a.retired_at IS NOT NULL AND b.public_id=$2 AND b.retired_at IS NULL`, d.ID, rotatedID.ID) != 1 {
		t.Fatal("device rotation lost identifier history")
	}
	rotatedPath := "/devices/" + rotatedID.ID.String()
	f.admin(t, http.MethodPatch, rotatedPath, map[string]any{"name": "renamed", "is_active": false}, &renamed, 200)
	if renamed.Name != "renamed" || renamed.Active {
		t.Fatal("device patch was not applied")
	}

	heartbeat := "/api/v1/devices/heartbeat"
	f.request(t, http.MethodPost, heartbeat, "", "", nil, nil, 401)
	f.request(t, http.MethodPost, heartbeat, "", strings.Repeat("a", 43), nil, nil, 401)
	f.request(t, http.MethodPost, heartbeat, "", d.APIKey, nil, nil, 401)
	f.admin(t, http.MethodPatch, rotatedPath, map[string]bool{"is_active": true}, nil, 200)
	f.request(t, http.MethodPost, heartbeat, "", d.APIKey, nil, nil, 204)

	var rotatedKey catalog.DeviceCredential
	f.admin(t, http.MethodPost, rotatedPath+"/keys/rotate", nil, &rotatedKey, 200)
	f.request(t, http.MethodPost, heartbeat, "", d.APIKey, nil, nil, 401)
	f.request(t, http.MethodPost, heartbeat, "", rotatedKey.APIKey, nil, nil, 204)
	if count(t, f.db, "SELECT count(*) FROM device_api_keys WHERE device_id=$1 AND revoked_at IS NOT NULL", internal) != 1 {
		t.Fatal("old key not revoked")
	}
}

func TestQueueValidationDedupAndDeadLetters(t *testing.T) {
	f := setup(t, 2)
	d, other := f.device(t), f.device(t)
	s, foreign := f.sensor(t, d.ID, "co2"), f.sensor(t, other.ID, "co2")
	now := time.Now().UTC().Add(-time.Second)
	f.submit(t, d.APIKey, reading(uuid.New(), foreign.ID, now, 700), 403)
	f.submit(t, d.APIKey, reading(uuid.New(), s.ID, now, -1), 400)
	f.submit(t, d.APIKey, reading(uuid.New(), s.ID, time.Now().Add(time.Hour), 700), 400)
	badMetric := reading(uuid.New(), s.ID, now, 20)
	badMetric.Measurements[0].Metric = "temperature"
	f.submit(t, d.APIKey, badMetric, 400)

	eventID := uuid.New()
	f.submit(t, d.APIKey, reading(eventID, s.ID, now, 700), 202)
	f.submit(t, d.APIKey, reading(eventID, s.ID, now, 700), 202)
	f.submit(t, d.APIKey, reading(uuid.New(), s.ID, now, 800), 503)
	if count(t, f.db, "SELECT count(*) FROM ingestion_events") != 0 {
		t.Fatal("HTTP 202 must be possible before any PostgreSQL commit")
	}
	f.startWorker(t, f.store, 4)
	f.drain(t)
	f.submit(t, d.APIKey, reading(eventID, s.ID, now, 800), 202)
	check(t, f.client.XAdd(t.Context(), &redis.XAddArgs{Stream: f.queue.Name(), Values: map[string]any{"event": "not JSON"}}).Err())
	f.drain(t)
	if count(t, f.db, "SELECT count(*) FROM ingestion_events") != 1 ||
		count(t, f.db, "SELECT count(*) FROM sensor_readings WHERE value=700") != 1 {
		t.Fatal("retry or conflict changed stored readings")
	}
	if count(t, f.db, "SELECT count(*) FROM ingestion_rejections WHERE reason IN ('conflicting_event_id','malformed_event')") != 2 {
		t.Fatal("missing dead-letter records")
	}
}

func TestRedisFailureReturns503(t *testing.T) {
	f := setup(t, 10)
	d := f.device(t)
	s := f.sensor(t, d.ID, "co2")
	client, err := stream.Open(os.Getenv("TEST_REDIS_URL"))
	check(t, err)
	check(t, client.Close())
	f.router = router.NewWithOptions(f.db, router.Options{
		AdminToken: adminToken, Producer: stream.New(client, f.queue.Name(), "workers", 10),
		RedisPing: func(ctx context.Context) error { return client.Ping(ctx).Err() },
	})
	ready := f.request(t, http.MethodGet, "/health/ready", "", "", nil, nil, 503)
	input := reading(uuid.New(), s.ID, time.Now().UTC(), 700)
	response := f.request(t, http.MethodPost, "/api/v1/readings", "", d.APIKey, input, nil, 503)
	if ready.Header().Get("Retry-After") != "2" || response.Header().Get("Retry-After") != "2" {
		t.Fatal("expected Retry-After on Redis dependency errors")
	}
	if depth, err := f.client.XLen(t.Context(), f.queue.Name()).Result(); err != nil || depth != 0 {
		t.Fatalf("failed enqueue produced stream depth %d, err %v", depth, err)
	}
}

func TestAuthenticationDatabaseSchemaErrorReturns500(t *testing.T) {
	f := setup(t, 10)
	d := f.device(t)
	_, err := f.db.Exec(t.Context(), "ALTER TABLE device_api_keys RENAME TO unavailable_keys")
	check(t, err)
	response := f.request(t, http.MethodPost, "/api/v1/devices/heartbeat", "", d.APIKey, nil, nil, 500)
	if response.Header().Get("Retry-After") != "" {
		t.Fatal("internal schema error must not include Retry-After")
	}
}

type capture struct{ event ingest.Event }

func (c *capture) Enqueue(_ context.Context, event ingest.Event) error {
	c.event = event
	return nil
}

type failingEvaluator struct{}

func (failingEvaluator) Evaluate(context.Context, pgx.Tx, uuid.UUID, float64, time.Time) error {
	return errors.New("injected alert evaluation failure")
}

func TestRollbackAndConcurrentDuplicateInsert(t *testing.T) {
	f := setup(t, 10)
	d := f.device(t)
	s := f.sensor(t, d.ID, "co2")
	device, err := catalog.New(catalog.NewPostgres(f.db)).Authenticate(t.Context(), d.APIKey)
	check(t, err)
	c := &capture{}
	service := ingest.New(f.store, c)
	at := time.Now().UTC()
	check(t, service.Submit(t.Context(), device, reading(uuid.New(), s.ID, at, 700)))
	first := c.event
	check(t, service.Submit(t.Context(), device, reading(uuid.New(), s.ID, at.Add(time.Millisecond), 800)))
	second := c.event
	failed := ingest.NewPostgres(f.db, failingEvaluator{}, nil)
	if err := failed.PersistBatch(t.Context(), []ingest.Event{first, second}); err == nil || err.Error() != "injected alert evaluation failure" {
		t.Fatalf("expected injected transaction failure, got %v", err)
	}
	if count(t, f.db, "SELECT count(*) FROM ingestion_events") != 0 || count(t, f.db, "SELECT count(*) FROM sensor_readings") != 0 {
		t.Fatal("event or reading survived transaction rollback")
	}
	start := make(chan struct{})
	results := make(chan error, 12)
	var calls sync.WaitGroup
	for i := range cap(results) {
		calls.Go(func() {
			<-start
			batch := []ingest.Event{first, second, first}
			if i%2 != 0 {
				batch = []ingest.Event{second, first}
			}
			results <- f.store.PersistBatch(t.Context(), batch)
		})
	}
	close(start)
	calls.Wait()
	close(results)
	for err := range results {
		check(t, err)
	}
	if count(t, f.db, "SELECT count(*) FROM ingestion_events") != 2 || count(t, f.db, "SELECT count(*) FROM sensor_readings") != 2 {
		t.Fatal("concurrent replay produced duplicates")
	}
}

func TestBatchConflictIsolatesNeighbors(t *testing.T) {
	f := setup(t, 10)
	d := f.device(t)
	s := f.sensor(t, d.ID, "co2")
	at, id := time.Now().UTC(), uuid.New()
	f.submit(t, d.APIKey, reading(id, s.ID, at, 700), 202)
	f.submit(t, d.APIKey, reading(id, s.ID, at, 800), 202)
	f.submit(t, d.APIKey, reading(uuid.New(), s.ID, at, 900), 202)
	f.startWorker(t, f.store, 1)
	f.drain(t)
	if count(t, f.db, "SELECT count(*) FROM ingestion_events") != 2 ||
		count(t, f.db, "SELECT count(*) FROM sensor_readings WHERE value IN (700,900)") != 2 ||
		count(t, f.db, "SELECT count(*) FROM ingestion_rejections WHERE reason='conflicting_event_id'") != 1 {
		t.Fatal("conflicting entry prevented adjacent events from committing or overwrote the first event")
	}
}

func TestBatchAlertTransitionsIgnoreOlderReadings(t *testing.T) {
	f := setup(t, 10)
	d := f.device(t)
	s := f.sensor(t, d.ID, "co2")
	rule := map[string]any{"operator": "gte", "threshold": 1000, "recovery_threshold": 900, "severity": "warning"}
	f.admin(t, http.MethodPost, "/sensors/"+s.ID.String()+"/alert-rules", rule, nil, 201)
	base := time.Now().UTC().Truncate(time.Microsecond).Add(-5 * time.Second)
	f.submit(t, d.APIKey, reading(uuid.New(), s.ID, base.Add(2*time.Second), 1200), 202)
	f.submit(t, d.APIKey, reading(uuid.New(), s.ID, base.Add(time.Second), 800), 202)
	f.submit(t, d.APIKey, reading(uuid.New(), s.ID, base.Add(3*time.Second), 850), 202)
	f.submit(t, d.APIKey, reading(uuid.New(), s.ID, base, 1200), 202)
	f.startWorker(t, f.store, 1)
	f.drain(t)
	var found []alert.Alert
	f.admin(t, http.MethodGet, "/alerts", nil, &found, 200)
	if len(found) != 1 || found[0].Status != "RESOLVED" || found[0].ResolvedAt == nil ||
		!found[0].TriggeredAt.Equal(base.Add(2*time.Second)) || !found[0].ResolvedAt.Equal(base.Add(3*time.Second)) {
		t.Fatalf("incorrect alert transitions in batch: %+v", found)
	}
	if count(t, f.db, "SELECT count(*) FROM sensor_readings") != 4 {
		t.Fatal("late readings did not remain in history")
	}
}

type interruptedStore struct {
	*ingest.Postgres
	allow    atomic.Bool
	attempts atomic.Int32
}

func (s *interruptedStore) PersistBatch(ctx context.Context, events []ingest.Event) error {
	s.attempts.Add(1)
	if !s.allow.Load() {
		return errors.New("injected retryable failure")
	}
	return s.Postgres.PersistBatch(ctx, events)
}

func TestWorkerLeavesFailurePendingAndReclaims(t *testing.T) {
	f := setup(t, 10)
	d := f.device(t)
	s := f.sensor(t, d.ID, "co2")
	processor := &interruptedStore{Postgres: f.store}
	f.submit(t, d.APIKey, reading(uuid.New(), s.ID, time.Now().UTC(), 700), 202)
	f.submit(t, d.APIKey, reading(uuid.New(), s.ID, time.Now().UTC(), 800), 202)
	f.startWorker(t, processor, 2)
	eventually(t, func() bool { return processor.attempts.Load() > 0 })
	pending, err := f.client.XPending(t.Context(), f.queue.Name(), "workers").Result()
	check(t, err)
	if pending.Count != 2 || count(t, f.db, "SELECT count(*) FROM sensor_readings") != 0 {
		t.Fatal("failure was acknowledged or not left pending")
	}
	processor.allow.Store(true)
	f.drain(t)
	pending, err = f.client.XPending(t.Context(), f.queue.Name(), "workers").Result()
	check(t, err)
	if pending.Count != 0 || count(t, f.db, "SELECT count(*) FROM sensor_readings") != 2 || processor.attempts.Load() < 2 {
		t.Fatal("pending message did not commit and acknowledge after recovery")
	}
}

func TestAlertOrderingAndUTCQuery(t *testing.T) {
	f := setup(t, 20)
	d := f.device(t)
	s := f.sensor(t, d.ID, "co2")
	now := time.Now().UTC()
	start := now.Truncate(time.Hour).Add(-time.Hour)
	var rule alert.Rule
	ruleBody := map[string]any{"operator": "gte", "threshold": 1000, "recovery_threshold": 900, "severity": "warning"}
	f.admin(t, http.MethodPost, "/sensors/"+s.ID.String()+"/alert-rules", ruleBody, &rule, 201)
	var rules []alert.Rule
	f.admin(t, http.MethodGet, "/sensors/"+s.ID.String()+"/alert-rules", nil, &rules, 200)
	if len(rules) != 1 || rules[0].ID != rule.ID {
		t.Fatal("rule missing from collection")
	}
	f.startWorker(t, f.store, 4)
	submitAt := func(at time.Time, value float64) {
		f.submit(t, d.APIKey, reading(uuid.New(), s.ID, at, value), 202)
		f.drain(t)
	}
	submitAt(start.Add(2*time.Minute), 1200)
	submitAt(start.Add(time.Minute), 800)
	submitAt(start.Add(2*time.Minute+time.Second), 950)
	var active []alert.Alert
	f.admin(t, http.MethodGet, "/alerts?status=ACTIVE", nil, &active, 200)
	if len(active) != 1 || active[0].RuleID != rule.ID {
		t.Fatal("late or hysteresis reading changed active alert")
	}
	submitAt(start.Add(3*time.Minute), 850)
	submitAt(start, 1200)
	var resolved alert.Alert
	f.admin(t, http.MethodGet, "/alerts/"+active[0].ID.String(), nil, &resolved, 200)
	if resolved.Status != "RESOLVED" || resolved.ResolvedAt == nil || resolved.Severity != "warning" ||
		count(t, f.db, "SELECT count(*) FROM alerts") != 1 {
		t.Fatal("alert was not resolved or an older reading reopened it")
	}
	f.admin(t, http.MethodPatch, "/alert-rules/"+rule.ID.String(), map[string]bool{"is_enabled": false}, nil, 200)

	base := "/devices/" + d.ID.String()
	filter := fmt.Sprintf("sensor_id=%s&from=%s&to=%s", s.ID, start.Format(time.RFC3339), start.Add(time.Hour).Format(time.RFC3339))
	var readings []history.Reading
	f.admin(t, http.MethodGet, base+"/readings?"+filter+"&limit=2&offset=1", nil, &readings, 200)
	if len(readings) != 2 || readings[0].SensorID != s.ID || !readings[0].RecordedAt.Equal(start.Add(2*time.Minute+time.Second)) {
		t.Fatal("history pagination or ordering incorrect")
	}
	var minutes, hours []history.Bucket
	f.admin(t, http.MethodGet, base+"/aggregations?"+filter+"&bucket=minute", nil, &minutes, 200)
	f.admin(t, http.MethodGet, base+"/aggregations?"+filter+"&bucket=hour", nil, &hours, 200)
	if len(minutes) != 4 || minutes[1].Count != 2 || minutes[1].Average != 1075 ||
		!minutes[1].Start.Equal(start.Add(2*time.Minute)) {
		t.Fatalf("wrong minute aggregation: %+v", minutes)
	}
	if len(hours) != 1 || hours[0].Count != 5 || hours[0].Min != 800 || hours[0].Max != 1200 || hours[0].Average != 1000 ||
		!hours[0].Start.Equal(start) {
		t.Fatalf("wrong hourly aggregation: %+v", hours)
	}
}

func TestConnectivityAndConcurrentSweeps(t *testing.T) {
	f := setup(t, 10)
	d := f.device(t)
	path := "/devices/" + d.ID.String() + "/status"
	var offline monitor.Status
	f.admin(t, http.MethodGet, path, nil, &offline, 200)
	if offline.Connectivity != "NEVER_SEEN" || offline.LastSeen != nil {
		t.Fatal("new devices must start at NEVER_SEEN")
	}
	f.request(t, http.MethodPost, "/api/v1/devices/heartbeat", "", d.APIKey, nil, nil, 204)
	f.admin(t, http.MethodGet, path, nil, &offline, 200)
	if offline.Connectivity != "ONLINE" || offline.LastSeen == nil {
		t.Fatal("heartbeat did not bring device online")
	}
	_, err := f.db.Exec(t.Context(), `UPDATE devices SET last_seen_at=clock_timestamp()-interval '2 minutes'`)
	check(t, err)

	var transitions atomic.Int64
	results := make(chan error, 4)
	var sweepers sync.WaitGroup
	for range cap(results) {
		sweepers.Go(func() {
			n, err := f.monitor.MarkOffline(t.Context())
			transitions.Add(n)
			results <- err
		})
	}
	sweepers.Wait()
	close(results)
	for err := range results {
		check(t, err)
	}
	f.admin(t, http.MethodGet, path, nil, &offline, 200)
	if transitions.Load() != 1 || offline.Connectivity != "OFFLINE" || !offline.Active ||
		count(t, f.db, "SELECT count(*) FROM devices WHERE connectivity_status='OFFLINE'") != 1 {
		t.Fatal("offline sweep was inconsistent")
	}
	f.request(t, http.MethodPost, "/api/v1/devices/heartbeat", "", d.APIKey, nil, nil, 204)
	f.admin(t, http.MethodGet, path, nil, &offline, 200)
	if offline.Connectivity != "ONLINE" {
		t.Fatal("second heartbeat did not restore connectivity")
	}
}

func TestVersionOneUpgradePreservesRecords(t *testing.T) {
	deviceID, sensorID, eventID := uuid.New(), uuid.New(), uuid.New()
	recorded := time.Date(2025, 4, 12, 8, 0, 0, 0, time.UTC)
	f := setupWithLegacyData(t, 10, func(db *pgxpool.Pool) {
		_, err := db.Exec(t.Context(), `WITH d AS (
			INSERT INTO devices (name, location, last_seen_at) VALUES ('legacy device','lab',clock_timestamp()) RETURNING id
		), di AS (
			INSERT INTO device_identifiers (device_id,public_id) SELECT id,$1 FROM d
		), s AS (
			INSERT INTO sensors (device_id,metric,unit) SELECT id,'co2','ppm' FROM d RETURNING id
		), si AS (
			INSERT INTO sensor_identifiers (sensor_id,public_id) SELECT id,$2 FROM s
		), e AS (
			INSERT INTO ingestion_events (device_id,event_id,payload_hash,recorded_at)
			SELECT id,$4,repeat('a',64),$3 FROM d RETURNING id
		), reading AS (
			INSERT INTO sensor_readings (ingestion_event_id,sensor_id,value,recorded_at)
			SELECT e.id,s.id,1200,$3 FROM s CROSS JOIN e
		), r AS (
			INSERT INTO alert_rules (sensor_id,condition,threshold,recovery_threshold,severity)
			SELECT id,'gt',1000,900,'critical' FROM s RETURNING id
		) INSERT INTO alerts (rule_id,triggered_value,triggered_at) SELECT id,1200,$3 FROM r`, deviceID, sensorID, recorded, eventID)
		check(t, err)
	})
	var device catalog.Device
	f.admin(t, http.MethodGet, "/devices/"+deviceID.String(), nil, &device, 200)
	var sensor catalog.Sensor
	f.admin(t, http.MethodGet, "/sensors/"+sensorID.String(), nil, &sensor, 200)
	var alerts []alert.Alert
	f.admin(t, http.MethodGet, "/alerts", nil, &alerts, 200)
	var watermark time.Time
	check(t, f.db.QueryRow(t.Context(), `SELECT last_evaluated_at FROM alert_rules`).Scan(&watermark))
	if device.ID != deviceID || sensor.ID != sensorID || sensor.DeviceID != deviceID ||
		len(alerts) != 1 || alerts[0].ID == uuid.Nil || alerts[0].Severity != "critical" || !watermark.Equal(recorded) {
		t.Fatal("upgrade changed legacy records or did not backfill alert state")
	}
	var hypertable bool
	check(t, f.db.QueryRow(t.Context(), `SELECT EXISTS (
		SELECT 1 FROM timescaledb_information.hypertables
		WHERE hypertable_schema=current_schema() AND hypertable_name='sensor_readings'
	)`).Scan(&hypertable))
	if !hypertable {
		t.Fatal("sensor_readings is not a TimescaleDB hypertable")
	}

	var reading history.Reading
	check(t, f.db.QueryRow(t.Context(), `SELECT e.event_id,i.public_id,r.recorded_at,r.value
		FROM sensor_readings r JOIN ingestion_events e ON e.id=r.ingestion_event_id
		JOIN sensor_identifiers i ON i.sensor_id=r.sensor_id`).Scan(
		&reading.EventID, &reading.SensorID, &reading.RecordedAt, &reading.Value))
	if reading.EventID != eventID || reading.SensorID != sensorID ||
		!reading.RecordedAt.Equal(recorded) || reading.Value != 1200 {
		t.Fatal("hypertable migration changed the legacy reading")
	}

	const replay = `INSERT INTO sensor_readings (ingestion_event_id,sensor_id,value,recorded_at)
		SELECT ingestion_event_id,sensor_id,value,$1 FROM sensor_readings LIMIT 1`
	for _, test := range []struct {
		recordedAt time.Time
		sqlState   string
	}{
		{recorded, "23505"},                     // unique_violation
		{recorded.Add(24 * time.Hour), "23503"}, // foreign_key_violation
	} {
		_, err := f.db.Exec(t.Context(), replay, test.recordedAt)
		var pgErr *pgconn.PgError
		if !errors.As(err, &pgErr) || pgErr.Code != test.sqlState {
			t.Fatalf("replay at %s: want SQLSTATE %s, got %v", test.recordedAt, test.sqlState, err)
		}
	}
}
