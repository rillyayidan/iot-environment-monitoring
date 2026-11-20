package stream

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/redis/go-redis/v9"
	"github.com/rillyayidan/iot-environment-monitoring/internal/ingest"
)

type Queue struct {
	client      *redis.Client
	name        string
	group       string
	limit       int
	enqueue     *redis.Script
	acknowledge *redis.Script
}

func Open(rawURL string) (*redis.Client, error) {
	options, err := redis.ParseURL(rawURL)
	if err != nil {
		return nil, errors.New("invalid REDIS_URL")
	}
	options.ContextTimeoutEnabled = true
	options.MaxRetries = -1
	options.DialTimeout = 2 * time.Second
	options.ReadTimeout = 3 * time.Second
	options.WriteTimeout = 3 * time.Second
	return redis.NewClient(options), nil
}

func New(client *redis.Client, name, group string, limit int) *Queue {
	return &Queue{
		client: client, name: name, group: group, limit: limit,
		enqueue: redis.NewScript(enqueueScript), acknowledge: redis.NewScript(acknowledgeScript),
	}
}

func (q *Queue) Name() string { return q.name }

// A full stream rejects new messages instead of trimming messages still awaiting commit.
const enqueueScript = `
if redis.call('XLEN', KEYS[1]) >= tonumber(ARGV[1]) then
	return redis.error_reply('QUEUE_FULL')
end
return redis.call('XADD', KEYS[1], '*', 'event', ARGV[2])`

func (q *Queue) Enqueue(ctx context.Context, event ingest.Event) error {
	data, err := json.Marshal(event)
	if err != nil {
		return err
	}
	if err := q.enqueue.Run(ctx, q.client, []string{q.name}, q.limit, data).Err(); err != nil {
		if err.Error() == "QUEUE_FULL" || err.Error() == "ERR QUEUE_FULL" {
			return ingest.ErrQueueFull
		}
		return fmt.Errorf("%w: %w", ingest.ErrUnavailable, err)
	}
	return nil
}

func (q *Queue) EnsureGroup(ctx context.Context) error {
	err := q.client.XGroupCreateMkStream(ctx, q.name, q.group, "0").Err()
	if err != nil && !strings.HasPrefix(err.Error(), "BUSYGROUP ") {
		return err
	}
	return nil
}

func (q *Queue) Read(ctx context.Context, consumer string, count int64) ([]redis.XMessage, error) {
	streams, err := q.client.XReadGroup(ctx, &redis.XReadGroupArgs{
		Group: q.group, Consumer: consumer, Streams: []string{q.name, ">"},
		Count: count, Block: time.Second,
	}).Result()
	if errors.Is(err, redis.Nil) {
		return nil, nil
	}
	if err != nil || len(streams) == 0 {
		return nil, err
	}
	return streams[0].Messages, nil
}

func (q *Queue) Claim(ctx context.Context, consumer, cursor string, idle time.Duration, count int64) ([]redis.XMessage, string, error) {
	return q.client.XAutoClaim(ctx, &redis.XAutoClaimArgs{
		Stream: q.name, Group: q.group, Consumer: consumer, Start: cursor, MinIdle: idle, Count: count,
	}).Result()
}

// This stream belongs to one consumer group. Deleting the acknowledged message frees capacity.
const acknowledgeScript = `
local acked = {}
for i = 2, #ARGV do
	if redis.call('XACK', KEYS[1], ARGV[1], ARGV[i]) == 1 then
		acked[#acked + 1] = ARGV[i]
	end
end
if #acked > 0 then
	redis.call('XDEL', KEYS[1], unpack(acked))
end
return #acked`

func (q *Queue) Ack(ctx context.Context, ids []string) error {
	if len(ids) == 0 {
		return nil
	}
	args := make([]any, 1, 1+len(ids))
	args[0] = q.group
	for _, id := range ids {
		args = append(args, id)
	}
	return q.acknowledge.Run(ctx, q.client, []string{q.name}, args...).Err()
}
