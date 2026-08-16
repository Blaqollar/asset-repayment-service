package queue

import (
	"context"
	"errors"
	"strconv"
	"sync"
	"time"

	"asset-repayment-service/internal/config"
	"asset-repayment-service/internal/domain/payment"
	"asset-repayment-service/internal/infrastructure/observability"
	pkgerrors "asset-repayment-service/pkg/errors"
	"github.com/redis/go-redis/v9"
	"go.uber.org/fx"
	"go.uber.org/zap"
)

// WorkerParams are the dependencies of the consumer pool.
type WorkerParams struct {
	fx.In

	Queue    payment.Queue `optional:"true"`
	Payments payment.Service
	Config   *config.Config
	Metrics  *observability.Metrics
	Logger   *zap.Logger
}

// StartWorkers runs the consumer pool for the application's lifetime.
func StartWorkers(params WorkerParams, lc fx.Lifecycle) {
	q, ok := params.Queue.(*Queue)
	if !ok || q == nil {
		return // no queue configured: payments are applied inline
	}

	logger := params.Logger.With(zap.String("module", "queue.worker"))
	ctx, cancel := context.WithCancel(context.Background())
	var wg sync.WaitGroup

	lc.Append(fx.Hook{
		OnStart: func(startCtx context.Context) error {
			if err := q.ensureGroup(startCtx); err != nil {
				return err
			}

			for i := 0; i < params.Config.Queue.Workers; i++ {
				w := &worker{
					queue:    q,
					payments: params.Payments,
					cfg:      params.Config,
					metrics:  params.Metrics,
					name:     "worker-" + strconv.Itoa(i),
					logger:   logger.With(zap.Int("worker", i)),
				}
				wg.Add(1)
				go func() {
					defer wg.Done()
					w.run(ctx)
				}()
			}

			// One reclaimer: without it, a worker dying mid-payment would
			// strand its message pending — a credit accepted and never applied.
			wg.Add(1)
			go func() {
				defer wg.Done()
				(&worker{
					queue:    q,
					payments: params.Payments,
					cfg:      params.Config,
					metrics:  params.Metrics,
					name:     "reclaimer",
					logger:   logger.With(zap.String("role", "reclaimer")),
				}).reclaim(ctx)
			}()

			logger.Info("queue workers started",
				zap.Int("workers", params.Config.Queue.Workers),
				zap.String("stream", q.stream),
				zap.String("group", q.group),
			)
			return nil
		},
		OnStop: func(context.Context) error {
			// Anything read but unacknowledged stays pending and is reclaimed
			// after restart, so shutdown costs a redelivery, never a credit.
			cancel()
			wg.Wait()
			logger.Info("queue workers stopped")
			return q.client.Close()
		},
	})
}

type worker struct {
	queue    *Queue
	payments payment.Service
	cfg      *config.Config
	metrics  *observability.Metrics
	name     string
	logger   *zap.Logger
}

// run consumes the stream until the context is cancelled.
func (w *worker) run(ctx context.Context) {
	for {
		if ctx.Err() != nil {
			return
		}

		streams, err := w.queue.client.XReadGroup(ctx, &redis.XReadGroupArgs{
			Group:    w.queue.group,
			Consumer: w.name,
			Streams:  []string{w.queue.stream, ">"},
			Count:    w.cfg.Queue.BatchSize,
			// Blocking, not polling: idle costs nothing, and no payment waits
			// for a tick.
			Block: w.cfg.Queue.BlockTimeout,
		}).Result()

		switch {
		case errors.Is(err, redis.Nil), errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
			continue
		case err != nil:
			if ctx.Err() != nil {
				return
			}
			// A broker that is down must not become a hot loop.
			w.logger.Warn("queue read failed", zap.Error(err))
			select {
			case <-ctx.Done():
				return
			case <-time.After(time.Second):
			}
			continue
		}

		for _, stream := range streams {
			for _, message := range stream.Messages {
				w.handle(ctx, message, 1)
			}
		}
	}
}

// handle applies one message and acknowledges it.
func (w *worker) handle(ctx context.Context, message redis.XMessage, attempt int64) {
	notification, err := decode(message.Values)
	if err != nil {
		w.logger.Error("undecodable queue message, dead-lettering",
			zap.String("message_id", message.ID), zap.Error(err))
		w.deadLetter(ctx, message, "undecodable")
		return
	}

	// Bounded: a worker stuck on one payment is not draining the rest.
	workCtx, cancel := context.WithTimeout(ctx, w.cfg.Queue.ProcessTimeout)
	defer cancel()

	started := time.Now()
	result, err := w.payments.Process(workCtx, notification)
	elapsed := time.Since(started)

	if err != nil {
		if permanent(err) {
			w.logger.Error("payment permanently rejected, dead-lettering",
				zap.String("transaction_reference", notification.Reference),
				zap.Error(err),
			)
			w.metrics.RecordPayment("dead_lettered", elapsed)
			w.deadLetter(ctx, message, err.Error())
			return
		}

		// Transient: left pending, and the reclaimer hands it back — the retry.
		w.metrics.RecordPayment("retry", elapsed)
		w.metrics.QueueRetries.Inc()
		w.logger.Warn("payment failed, leaving for redelivery",
			zap.String("transaction_reference", notification.Reference),
			zap.Int64("attempt", attempt),
			zap.Error(err),
		)

		if attempt >= w.cfg.Queue.MaxAttempts {
			w.logger.Error("payment exhausted its attempts, dead-lettering",
				zap.String("transaction_reference", notification.Reference),
				zap.Int64("attempts", attempt),
			)
			w.deadLetter(ctx, message, err.Error())
		}
		return
	}

	w.metrics.RecordPayment(result.Outcome, elapsed)
	if result.Applied() && result.Payment != nil {
		w.metrics.AmountApplied.Add(result.Payment.Applied.InexactFloat64())
	}
	w.ack(ctx, message.ID)

	w.logger.Debug("payment processed from queue",
		zap.String("transaction_reference", notification.Reference),
		zap.String("outcome", result.Outcome),
		zap.Duration("elapsed", elapsed),
	)
}

// reclaim hands back messages whose worker died before acknowledging.
func (w *worker) reclaim(ctx context.Context) {
	ticker := time.NewTicker(w.cfg.Queue.ClaimInterval)
	defer ticker.Stop()

	cursor := "0-0"
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}

		messages, next, err := w.queue.client.XAutoClaim(ctx, &redis.XAutoClaimArgs{
			Stream:   w.queue.stream,
			Group:    w.queue.group,
			Consumer: w.name,
			MinIdle:  w.cfg.Queue.ClaimMinIdle,
			Start:    cursor,
			Count:    w.cfg.Queue.BatchSize,
		}).Result()
		if err != nil {
			if ctx.Err() == nil && !errors.Is(err, redis.Nil) {
				w.logger.Warn("reclaim failed", zap.Error(err))
			}
			continue
		}

		cursor = next
		if next == "" || next == "0-0" {
			cursor = "0-0"
		}

		for _, message := range messages {
			w.metrics.QueueReclaimed.Inc()
			w.handle(ctx, message, w.deliveries(ctx, message.ID))
		}

		w.observeDepth(ctx)
	}
}

// deliveries counts hand-outs, so a poison message can be retired.
func (w *worker) deliveries(ctx context.Context, id string) int64 {
	pending, err := w.queue.client.XPendingExt(ctx, &redis.XPendingExtArgs{
		Stream: w.queue.stream,
		Group:  w.queue.group,
		Start:  id,
		End:    id,
		Count:  1,
	}).Result()
	if err != nil || len(pending) == 0 {
		return 1
	}
	return pending[0].RetryCount
}

// deadLetter moves a message aside and acknowledges the original
func (w *worker) deadLetter(ctx context.Context, message redis.XMessage, reason string) {
	values := map[string]any{"reason": reason, "original_id": message.ID}
	for k, v := range message.Values {
		values[k] = v
	}

	if err := w.queue.client.XAdd(ctx, &redis.XAddArgs{
		Stream: w.queue.deadStream,
		Values: values,
	}).Err(); err != nil {
		// Not acknowledged: better visibly stuck than silently dropped.
		w.logger.Error("dead-letter write failed, leaving message pending",
			zap.String("message_id", message.ID), zap.Error(err))
		return
	}

	w.metrics.QueueDeadLettered.Inc()
	w.ack(ctx, message.ID)
}

func (w *worker) ack(ctx context.Context, id string) {
	// A fresh context: a cancelled one would leave a finished payment pending.
	ackCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), w.queue.timeout)
	defer cancel()

	if err := w.queue.client.XAck(ackCtx, w.queue.stream, w.queue.group, id).Err(); err != nil {
		w.logger.Warn("ack failed; message will be redelivered",
			zap.String("message_id", id), zap.Error(err))
	}
}

// observeDepth samples the backlog.
func (w *worker) observeDepth(ctx context.Context) {
	depth, err := w.queue.client.XLen(ctx, w.queue.stream).Result()
	if err == nil {
		w.metrics.QueueDepth.Set(float64(depth))
	}

	pending, err := w.queue.client.XPending(ctx, w.queue.stream, w.queue.group).Result()
	if err == nil && pending != nil {
		w.metrics.QueuePending.Set(float64(pending.Count))
	}
}

// permanent reports whether a retry could ever produce a different answer.
func permanent(err error) bool {
	var domainErr *pkgerrors.DomainError
	if errors.As(err, &domainErr) {
		return domainErr.Code >= 400 && domainErr.Code < 500
	}
	return false
}
