package alert

import (
	"context"
	"fmt"
	"math"
	"time"

	"github.com/google/uuid"
	"github.com/rillyayidan/iot-environment-monitoring/internal/catalog"
)

type Policy struct {
	Operator  string  `json:"operator"`
	Threshold float64 `json:"threshold"`
	Recovery  float64 `json:"recovery_threshold"`
	Severity  string  `json:"severity"`
	Enabled   bool    `json:"is_enabled"`
}

type Rule struct {
	ID       uuid.UUID `json:"id"`
	SensorID uuid.UUID `json:"sensor_id"`
	Policy
}

type Input struct {
	Operator  *string  `json:"operator"`
	Threshold *float64 `json:"threshold"`
	Recovery  *float64 `json:"recovery_threshold"`
	Severity  *string  `json:"severity"`
	Enabled   *bool    `json:"is_enabled"`
}

type Alert struct {
	ID          uuid.UUID  `json:"id"`
	RuleID      uuid.UUID  `json:"rule_id"`
	SensorID    uuid.UUID  `json:"sensor_id"`
	DeviceID    uuid.UUID  `json:"device_id"`
	Severity    string     `json:"severity"`
	Status      string     `json:"status"`
	Value       float64    `json:"triggered_value"`
	TriggeredAt time.Time  `json:"triggered_at"`
	ResolvedAt  *time.Time `json:"resolved_at"`
}

type Repository interface {
	CreateRule(context.Context, uuid.UUID, Policy) (Rule, error)
	ListRules(context.Context, uuid.UUID, int, int) ([]Rule, error)
	PatchRule(context.Context, uuid.UUID, Input) (Rule, error)
	List(context.Context, string, int, int) ([]Alert, error)
	Get(context.Context, uuid.UUID) (Alert, error)
}

type Service struct {
	repo Repository
}

func New(repo Repository) *Service { return &Service{repo: repo} }

func (p Policy) Triggers(value float64) bool {
	switch p.Operator {
	case "gt":
		return value > p.Threshold
	case "gte":
		return value >= p.Threshold
	case "lt":
		return value < p.Threshold
	case "lte":
		return value <= p.Threshold
	default:
		return false
	}
}

func (p Policy) Recovers(value float64) bool {
	if p.Operator == "gt" || p.Operator == "gte" {
		return value <= p.Recovery
	}
	return value >= p.Recovery
}

func (p Policy) validate() error {
	switch p.Operator {
	case "gt", "gte", "lt", "lte":
	default:
		return fmt.Errorf("%w: unsupported alert operator", catalog.ErrInvalid)
	}
	if p.Severity != "warning" && p.Severity != "critical" {
		return fmt.Errorf("%w: severity must be warning or critical", catalog.ErrInvalid)
	}
	if math.IsNaN(p.Threshold) || math.IsInf(p.Threshold, 0) ||
		math.IsNaN(p.Recovery) || math.IsInf(p.Recovery, 0) || p.Triggers(p.Recovery) {
		return fmt.Errorf("%w: recovery_threshold must be finite and outside the trigger range", catalog.ErrInvalid)
	}
	return nil
}

func (i Input) apply(p Policy) (Policy, error) {
	if i.Operator != nil {
		p.Operator = *i.Operator
	}
	if i.Threshold != nil {
		p.Threshold = *i.Threshold
	}
	if i.Recovery != nil {
		p.Recovery = *i.Recovery
	}
	if i.Severity != nil {
		p.Severity = *i.Severity
	}
	if i.Enabled != nil {
		p.Enabled = *i.Enabled
	}
	return p, p.validate()
}

func (s *Service) CreateRule(ctx context.Context, sensor uuid.UUID, input Input) (Rule, error) {
	if input.Operator == nil || input.Threshold == nil || input.Recovery == nil || input.Severity == nil {
		return Rule{}, fmt.Errorf("%w: operator, threshold, recovery_threshold and severity are required", catalog.ErrInvalid)
	}
	policy, err := input.apply(Policy{Enabled: true})
	if err != nil {
		return Rule{}, err
	}
	return s.repo.CreateRule(ctx, sensor, policy)
}

func (s *Service) ListRules(ctx context.Context, sensor uuid.UUID, limit, offset int) ([]Rule, error) {
	return s.repo.ListRules(ctx, sensor, limit, offset)
}

func (s *Service) PatchRule(ctx context.Context, id uuid.UUID, input Input) (Rule, error) {
	if input == (Input{}) {
		return Rule{}, fmt.Errorf("%w: at least one alert rule field is required", catalog.ErrInvalid)
	}
	return s.repo.PatchRule(ctx, id, input)
}

func (s *Service) List(ctx context.Context, status string, limit, offset int) ([]Alert, error) {
	switch status {
	case "", "ACTIVE", "RESOLVED":
		return s.repo.List(ctx, status, limit, offset)
	default:
		return nil, fmt.Errorf("%w: status must be ACTIVE or RESOLVED", catalog.ErrInvalid)
	}
}

func (s *Service) Get(ctx context.Context, id uuid.UUID) (Alert, error) {
	return s.repo.Get(ctx, id)
}
