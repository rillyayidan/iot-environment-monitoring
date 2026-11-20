package history

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/rillyayidan/iot-environment-monitoring/internal/catalog"
)

type Filter struct {
	Device uuid.UUID
	Sensor *uuid.UUID
	From   time.Time
	To     time.Time
	Limit  int
	Offset int
}

type Reading struct {
	EventID    uuid.UUID `json:"event_id"`
	SensorID   uuid.UUID `json:"sensor_id"`
	Metric     string    `json:"metric"`
	Unit       string    `json:"unit"`
	Value      float64   `json:"value"`
	RecordedAt time.Time `json:"recorded_at"`
}

type Bucket struct {
	SensorID uuid.UUID `json:"sensor_id"`
	Metric   string    `json:"metric"`
	Unit     string    `json:"unit"`
	Start    time.Time `json:"bucket_start"`
	Average  float64   `json:"avg"`
	Min      float64   `json:"min"`
	Max      float64   `json:"max"`
	Count    int64     `json:"count"`
}

type Repository interface {
	Readings(context.Context, Filter) ([]Reading, error)
	Aggregations(context.Context, Filter, string) ([]Bucket, error)
}

type Service struct {
	repo Repository
}

func New(repo Repository) *Service { return &Service{repo: repo} }

func validate(filter Filter) (Filter, error) {
	if filter.To.IsZero() {
		filter.To = time.Now().UTC()
	}
	if filter.From.IsZero() {
		filter.From = filter.To.Add(-24 * time.Hour)
	}
	if !filter.From.Before(filter.To) || filter.To.Sub(filter.From) > 31*24*time.Hour {
		return Filter{}, fmt.Errorf("%w: from must precede to and the range is capped at 31 days", catalog.ErrInvalid)
	}
	filter.From, filter.To = filter.From.UTC(), filter.To.UTC()
	return filter, nil
}

func (s *Service) Readings(ctx context.Context, filter Filter) ([]Reading, error) {
	filter, err := validate(filter)
	if err != nil {
		return nil, err
	}
	return s.repo.Readings(ctx, filter)
}

func (s *Service) Aggregations(ctx context.Context, filter Filter, bucket string) ([]Bucket, error) {
	filter, err := validate(filter)
	if err != nil {
		return nil, err
	}
	switch bucket {
	case "minute":
		return s.repo.Aggregations(ctx, filter, "1 minute")
	case "hour":
		return s.repo.Aggregations(ctx, filter, "1 hour")
	default:
		return nil, fmt.Errorf("%w: bucket must be minute or hour", catalog.ErrInvalid)
	}
}
