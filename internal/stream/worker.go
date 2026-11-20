package stream

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/redis/go-redis/v9"
	"github.com/rillyayidan/iot-environment-monitoring/internal/ingest"
)

type Processor interface {
	PersistBatch(context.Context, []ingest.Event) error
	Reject(context.Context, string, string, string, string) error
}

type Worker struct {
	queue          *Queue
	store          Processor
	parallel       int
	batchSize      int64
	claimIdle      time.Duration
	processTimeout time.Duration
}

func NewWorker(queue *Queue, store Processor, parallel, batchSize int, claimIdle, processTimeout time.Duration) *Worker {
	return &Worker{queue: queue, store: store, parallel: parallel, batchSize: int64(batchSize), claimIdle: claimIdle, processTimeout: processTimeout}
}

func wait(ctx context.Context, delay time.Duration) {
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
	case <-timer.C:
	}
}

func (w *Worker) Run(ctx context.Context) {
	instance := uuid.New().String()
	var workers sync.WaitGroup
	for n := 0; n < w.parallel; n++ {
		workers.Go(func() {
			w.consume(ctx, fmt.Sprintf("%s-%d", instance, n))
		})
	}
	workers.Wait()
}

func (w *Worker) consume(ctx context.Context, consumer string) {
	ready := false
	cursor := "0-0"
	var nextClaim time.Time
	delay := 250 * time.Millisecond
	for ctx.Err() == nil {
		err := func() error {
			if !ready {
				if err := w.queue.EnsureGroup(ctx); err != nil {
					return err
				}
				ready = true
			}

			if !time.Now().Before(nextClaim) {
				recovered, next, err := w.queue.Claim(ctx, consumer, cursor, w.claimIdle, w.batchSize)
				if err != nil {
					return err
				}
				cursor = next
				if next == "0-0" {
					nextClaim = time.Now().Add(min(time.Second, w.claimIdle))
				}
				if len(recovered) > 0 {
					w.handle(ctx, recovered)
					return nil
				}
			}
			messages, err := w.queue.Read(ctx, consumer, w.batchSize)
			if err != nil {
				return err
			}
			w.handle(ctx, messages)
			return nil
		}()
		if err == nil {
			delay = 250 * time.Millisecond
			continue
		}
		if ctx.Err() == nil {
			slog.Warn("stream read failed; retrying", "consumer", consumer, "error_type", fmt.Sprintf("%T", err))
		}
		ready = false
		cursor = "0-0"
		nextClaim = time.Time{}
		wait(ctx, delay)
		delay = min(5*time.Second, 2*delay)
	}
}

func (w *Worker) handle(ctx context.Context, messages []redis.XMessage) {
	if len(messages) == 0 {
		return
	}
	op, cancel := context.WithTimeout(ctx, w.processTimeout)
	defer cancel()
	acked := make([]string, 0, len(messages))
	valid := make([]redis.XMessage, 0, len(messages))
	events := make([]ingest.Event, 0, len(messages))
	for _, message := range messages {
		raw, ok := message.Values["event"].(string)
		var event ingest.Event
		if !ok || len(raw) > 64<<10 || json.Unmarshal([]byte(raw), &event) != nil || event.Validate() != nil {
			if w.reject(op, message, "malformed_event", "") {
				acked = append(acked, message.ID)
			}
			continue
		}
		valid = append(valid, message)
		events = append(events, event)
	}

	if len(events) > 0 {
		err := w.store.PersistBatch(op, events)
		switch {
		case err == nil:
			for _, message := range valid {
				acked = append(acked, message.ID)
			}
		case rejectionReason(err) != "":
			// The batch rolled back. Retry in delivery order to isolate the bad entry.
			for i, message := range valid {
				if op.Err() != nil {
					break
				}
				singleErr := err
				if len(events) > 1 {
					singleErr = w.store.PersistBatch(op, events[i:i+1])
				}
				if singleErr == nil || w.rejectEvent(op, message, singleErr) {
					acked = append(acked, message.ID)
				}
			}
		default:
			logPending(ctx, messages[0].ID, len(events), err)
		}
	}
	if err := w.queue.Ack(op, acked); err != nil {
		logPending(ctx, acked[0], len(acked), err)
	}
}

func rejectionReason(err error) string {
	switch {
	case errors.Is(err, ingest.ErrConflict):
		return "conflicting_event_id"
	case errors.Is(err, ingest.ErrInvalidEvent):
		return "invalid_sensor_reference"
	default:
		return ""
	}
}

func (w *Worker) rejectEvent(ctx context.Context, message redis.XMessage, err error) bool {
	if reason := rejectionReason(err); reason != "" {
		return w.reject(ctx, message, reason, message.Values["event"].(string))
	}
	logPending(ctx, message.ID, 1, err)
	return false
}

func (w *Worker) reject(ctx context.Context, message redis.XMessage, reason, payload string) bool {
	err := w.store.Reject(ctx, w.queue.Name(), message.ID, reason, payload)
	if err != nil {
		logPending(ctx, message.ID, 1, err)
	}
	return err == nil
}

func logPending(ctx context.Context, firstID string, count int, err error) {
	if ctx.Err() == nil {
		slog.Warn("events left pending for retry", "first_message_id", firstID, "count", count, "error_type", fmt.Sprintf("%T", err))
	}
}
