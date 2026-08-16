package queue

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"asset-repayment-service/internal/config"
	"asset-repayment-service/internal/domain/payment"
	"asset-repayment-service/internal/infrastructure/observability"
	"github.com/redis/go-redis/v9"
	"go.uber.org/fx"
	"go.uber.org/zap"
)

// payloadField is the single stream field each message carries.
const payloadField = "notification"

// Queue is the Redis stream implementation of payment.Queue.
type Queue struct {
	client  *redis.Client
	metrics *observability.Metrics
	logger  *zap.Logger

	stream     string
	deadStream string
	group      string
	maxLen     int64
	timeout    time.Duration
}

// New builds the queue, or returns nil when it is switched off — the default,
// in which the payment service applies inline.
func New(cfg *config.Config, metrics *observability.Metrics, logger *zap.Logger) (payment.Queue, error) {
	log := logger.With(zap.String("module", "queue"))

	if !cfg.QueueEnabled() {
		log.Info("queue disabled, payments are applied synchronously")
		return nil, nil
	}

	client := redis.NewClient(&redis.Options{
		Addr:         cfg.Redis.Address,
		Password:     cfg.Redis.Password,
		DB:           cfg.Redis.DB,
		PoolSize:     cfg.Redis.PoolSize,
		DialTimeout:  2 * time.Second,
		ReadTimeout:  cfg.Queue.BlockTimeout + time.Second,
		WriteTimeout: cfg.Queue.Timeout,
	})

	q := &Queue{
		client:     client,
		metrics:    metrics,
		logger:     log,
		stream:     cfg.Queue.Stream,
		deadStream: cfg.Queue.Stream + ":dead",
		group:      cfg.Queue.Group,
		maxLen:     cfg.Queue.MaxLen,
		timeout:    cfg.Queue.Timeout,
	}
	return q, nil
}

// Enqueue durably appends the notification.
func (q *Queue) Enqueue(ctx context.Context, notification payment.ValidatedNotification) error {
	encoded, err := json.Marshal(notification)
	if err != nil {
		return fmt.Errorf("encode notification: %w", err)
	}

	opCtx, cancel := context.WithTimeout(ctx, q.timeout)
	defer cancel()

	if err := q.client.XAdd(opCtx, &redis.XAddArgs{
		Stream: q.stream,
		MaxLen: q.maxLen,
		Approx: true,
		Values: map[string]any{payloadField: encoded},
	}).Err(); err != nil {
		q.metrics.QueueEnqueueFailures.Inc()
		return fmt.Errorf("enqueue payment: %w", err)
	}

	q.metrics.QueueEnqueued.Inc()
	return nil
}

// ensureGroup creates the consumer group, and the stream with it.
func (q *Queue) ensureGroup(ctx context.Context) error {
	err := q.client.XGroupCreateMkStream(ctx, q.stream, q.group, "0").Err()
	if err != nil && err.Error() != "BUSYGROUP Consumer Group name already exists" {
		return fmt.Errorf("create consumer group: %w", err)
	}
	return nil
}

// decode reads a notification back off the stream.
func decode(values map[string]any) (payment.ValidatedNotification, error) {
	var notification payment.ValidatedNotification

	raw, ok := values[payloadField].(string)
	if !ok {
		return notification, fmt.Errorf("message has no %q field", payloadField)
	}
	if err := json.Unmarshal([]byte(raw), &notification); err != nil {
		return notification, fmt.Errorf("decode notification: %w", err)
	}
	return notification, nil
}

// Module wires the queue and the workers that drain it.
var Module = fx.Module("queue", fx.Options(
	fx.Provide(New),
	fx.Invoke(StartWorkers),
))
