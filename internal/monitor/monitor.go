package monitor

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/google/uuid"
)

type Status struct {
	ID           uuid.UUID  `json:"id"`
	Active       bool       `json:"is_active"`
	Connectivity string     `json:"connectivity"`
	LastSeen     *time.Time `json:"last_seen_at"`
}

type Repository interface {
	Heartbeat(context.Context, uuid.UUID) error
	Status(context.Context, uuid.UUID) (Status, error)
	MarkOffline(context.Context) (int64, error)
}

type Service struct {
	repo Repository
}

func New(repo Repository) *Service { return &Service{repo: repo} }

func (s *Service) Heartbeat(ctx context.Context, device uuid.UUID) error {
	return s.repo.Heartbeat(ctx, device)
}

func (s *Service) Status(ctx context.Context, id uuid.UUID) (Status, error) {
	return s.repo.Status(ctx, id)
}

func (s *Service) Run(ctx context.Context, interval time.Duration) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for ctx.Err() == nil {
		op, cancel := context.WithTimeout(ctx, 5*time.Second)
		count, err := s.repo.MarkOffline(op)
		cancel()
		if err != nil && ctx.Err() == nil {
			slog.Warn("offline sweep failed", "error_type", fmt.Sprintf("%T", err))
		}
		if count > 0 {
			slog.Info("marked devices offline", "count", count)
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}
