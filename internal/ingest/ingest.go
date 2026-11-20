package ingest

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"slices"
	"time"

	"github.com/google/uuid"
	"github.com/rillyayidan/iot-environment-monitoring/internal/catalog"
)

var (
	ErrOwnership    = errors.New("sensor is unavailable for this device")
	ErrConflict     = errors.New("event_id conflicts with a persisted event")
	ErrInvalidEvent = errors.New("invalid queued event")
	ErrUnavailable  = errors.New("ingestion queue unavailable")
	ErrQueueFull    = errors.New("ingestion queue is full")
)

const maxMeasurements = 50

type Input struct {
	EventID      uuid.UUID          `json:"event_id"`
	RecordedAt   time.Time          `json:"recorded_at"`
	Measurements []MeasurementInput `json:"measurements"`
}

type MeasurementInput struct {
	SensorID uuid.UUID `json:"sensor_id"`
	Metric   string    `json:"metric"`
	Value    *float64  `json:"value"`
}

type Measurement struct {
	SensorID uuid.UUID `json:"sensor_id"`
	Metric   string    `json:"metric"`
	Value    float64   `json:"value"`
}

type Event struct {
	DeviceID     uuid.UUID     `json:"device_id"`
	EventID      uuid.UUID     `json:"event_id"`
	RecordedAt   time.Time     `json:"recorded_at"`
	ReceivedAt   time.Time     `json:"received_at"`
	Measurements []Measurement `json:"measurements"`
	Hash         string        `json:"payload_hash"`
}

type OwnedSensor struct {
	InternalID uuid.UUID
	Metric     string
}

type SensorLookup interface {
	OwnedSensors(context.Context, uuid.UUID, []uuid.UUID) (map[uuid.UUID]OwnedSensor, error)
}

type Producer interface {
	Enqueue(context.Context, Event) error
}

type Service struct {
	sensors  SensorLookup
	producer Producer
}

func New(sensors SensorLookup, producer Producer) *Service {
	return &Service{sensors: sensors, producer: producer}
}

func ValidValue(metric string, value float64) bool {
	if math.IsNaN(value) || math.IsInf(value, 0) {
		return false
	}
	switch metric {
	case "co2":
		return value >= 0 && value <= 100000
	case "temperature":
		return value >= -100 && value <= 150
	case "humidity":
		return value >= 0 && value <= 100
	default:
		return false
	}
}

func (s *Service) Submit(ctx context.Context, device uuid.UUID, input Input) error {
	now := time.Now().UTC()
	recordedAt := input.RecordedAt.UTC()
	if input.EventID == uuid.Nil || recordedAt.IsZero() || recordedAt.Year() < 2000 ||
		recordedAt.After(now.Add(5*time.Minute)) {
		return fmt.Errorf("%w: event_id and a valid recorded_at are required", catalog.ErrInvalid)
	}
	if len(input.Measurements) < 1 || len(input.Measurements) > maxMeasurements {
		return fmt.Errorf("%w: expected 1 to %d measurements", catalog.ErrInvalid, maxMeasurements)
	}
	ids := make([]uuid.UUID, 0, len(input.Measurements))
	seen := make(map[uuid.UUID]bool, len(input.Measurements))
	for _, item := range input.Measurements {
		if item.SensorID == uuid.Nil || item.Value == nil || !ValidValue(item.Metric, *item.Value) || seen[item.SensorID] {
			return fmt.Errorf("%w: each measurement requires a unique sensor_id, supported metric and in-range value", catalog.ErrInvalid)
		}
		seen[item.SensorID] = true
		ids = append(ids, item.SensorID)
	}
	owned, err := s.sensors.OwnedSensors(ctx, device, ids)
	if err != nil {
		return err
	}
	event := Event{DeviceID: device, EventID: input.EventID, RecordedAt: recordedAt, ReceivedAt: now}
	for _, item := range input.Measurements {
		sensor, ok := owned[item.SensorID]
		if !ok {
			return ErrOwnership
		}
		if item.Metric != sensor.Metric {
			return fmt.Errorf("%w: measurement metric does not match sensor", catalog.ErrInvalid)
		}
		event.Measurements = append(event.Measurements, Measurement{SensorID: sensor.InternalID, Metric: item.Metric, Value: *item.Value})
	}
	event.Hash, err = payloadHash(event)
	if err != nil {
		return err
	}
	return s.producer.Enqueue(ctx, event)
}

func payloadHash(event Event) (string, error) {
	items := slices.Clone(event.Measurements)
	slices.SortFunc(items, func(a, b Measurement) int { return bytes.Compare(a.SensorID[:], b.SensorID[:]) })
	for i := range items {
		if items[i].Value == 0 {
			items[i].Value = 0
		}
	}
	payload := struct {
		RecordedAt   time.Time     `json:"recorded_at"`
		Measurements []Measurement `json:"measurements"`
	}{event.RecordedAt.UTC(), items}
	data, err := json.Marshal(payload)
	if err != nil {
		return "", fmt.Errorf("encode canonical event: %w", err)
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:]), nil
}

func (event Event) Validate() error {
	if event.DeviceID == uuid.Nil || event.EventID == uuid.Nil || event.RecordedAt.IsZero() ||
		event.ReceivedAt.IsZero() || event.RecordedAt.Year() < 2000 ||
		event.RecordedAt.After(event.ReceivedAt.Add(5*time.Minute)) ||
		len(event.Measurements) < 1 || len(event.Measurements) > maxMeasurements {
		return ErrInvalidEvent
	}
	seen := make(map[uuid.UUID]bool, len(event.Measurements))
	for _, m := range event.Measurements {
		if m.SensorID == uuid.Nil || seen[m.SensorID] || !ValidValue(m.Metric, m.Value) {
			return ErrInvalidEvent
		}
		seen[m.SensorID] = true
	}
	hash, err := payloadHash(event)
	if err != nil || hash != event.Hash {
		return ErrInvalidEvent
	}
	return nil
}
