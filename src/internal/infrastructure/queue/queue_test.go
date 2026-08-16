package queue

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"testing"
	"time"

	"asset-repayment-service/internal/config"
	"asset-repayment-service/internal/domain/payment"
	"asset-repayment-service/internal/infrastructure/observability"
	pkgerrors "asset-repayment-service/pkg/errors"
	"asset-repayment-service/pkg/money"
	"github.com/alicebob/miniredis/v2"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/fx"
	"go.uber.org/zap"
)

// processor is a stand-in for the payment domain service. The queue's job is
// to deliver each notification to it exactly as accepted, and to acknowledge
// or retain the message according to what it returns — that is all these tests
// assert on.
type processor struct {
	mu       sync.Mutex
	seen     []payment.ValidatedNotification
	err      error
	released chan struct{}
}

func (p *processor) Accept(context.Context, payment.Notification, json.RawMessage) (*payment.Result, error) {
	return nil, errors.New("not used")
}

func (p *processor) Process(_ context.Context, v payment.ValidatedNotification) (*payment.Result, error) {
	p.mu.Lock()
	p.seen = append(p.seen, v)
	err := p.err
	p.mu.Unlock()

	select {
	case p.released <- struct{}{}:
	default:
	}

	if err != nil {
		return nil, err
	}
	return &payment.Result{Outcome: payment.OutcomeApplied}, nil
}

func (p *processor) count() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return len(p.seen)
}

// harness wires a queue and its workers against an in-process Redis, so these
// tests exercise the real stream commands — consumer groups, acknowledgement,
// reclaim — rather than a mock that agrees with itself.
type harness struct {
	queue     *Queue
	processor *processor
	metrics   *observability.Metrics
	server    *miniredis.Miniredis
	lifecycle *fxtestLifecycle
}

func newHarness(t *testing.T, tune func(*config.Config)) *harness {
	t.Helper()

	server := miniredis.RunT(t)

	cfg := &config.Config{}
	cfg.Redis.Address = server.Addr()
	cfg.Queue.Enabled = true
	cfg.Queue.Stream = "payments:test"
	cfg.Queue.Group = "workers"
	cfg.Queue.Workers = 1
	cfg.Queue.BatchSize = 16
	cfg.Queue.BlockTimeout = 50 * time.Millisecond
	cfg.Queue.ProcessTimeout = 2 * time.Second
	cfg.Queue.Timeout = time.Second
	cfg.Queue.ClaimMinIdle = 10 * time.Millisecond
	cfg.Queue.ClaimInterval = 50 * time.Millisecond
	cfg.Queue.MaxAttempts = 3
	cfg.Queue.MaxLen = 1000
	if tune != nil {
		tune(cfg)
	}

	metrics := observability.NewMetrics()
	logger := zap.NewNop()

	q, err := New(cfg, metrics, logger)
	require.NoError(t, err)
	require.NotNil(t, q)

	proc := &processor{released: make(chan struct{}, 64)}
	lc := &fxtestLifecycle{}
	StartWorkers(WorkerParams{
		Queue:    q,
		Payments: proc,
		Config:   cfg,
		Metrics:  metrics,
		Logger:   logger,
	}, lc)

	require.NoError(t, lc.start(context.Background()))
	t.Cleanup(func() { _ = lc.stop(context.Background()) })

	return &harness{
		queue:     q.(*Queue),
		processor: proc,
		metrics:   metrics,
		server:    server,
		lifecycle: lc,
	}
}

func notification(reference string) payment.ValidatedNotification {
	ngn := money.Lookup("NGN")
	return payment.ValidatedNotification{
		CustomerID:      "GIG00001",
		Reference:       reference,
		Currency:        ngn,
		Amount:          money.FromInt(10_000),
		ProviderStatus:  "COMPLETE",
		TransactionDate: time.Date(2025, 11, 7, 13, 54, 16, 0, time.UTC),
		ReceivedAt:      time.Date(2025, 11, 7, 13, 54, 17, 0, time.UTC),
	}
}

// waitFor polls until cond holds, so a test asserts on an outcome rather than
// on a sleep long enough to hope for one.
func waitFor(t *testing.T, cond func() bool, msg string) {
	t.Helper()

	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", msg)
}

func TestEnqueuedPaymentReachesAWorkerIntact(t *testing.T) {
	h := newHarness(t, nil)

	sent := notification("VPAY-QUEUE-000001")
	require.NoError(t, h.queue.Enqueue(context.Background(), sent))

	waitFor(t, func() bool { return h.processor.count() == 1 }, "the notification to be processed")

	got := h.processor.seen[0]
	assert.Equal(t, sent.Reference, got.Reference)
	assert.Equal(t, sent.CustomerID, got.CustomerID)
	assert.Equal(t, "NGN", got.Currency.Code)
	assert.True(t, sent.Amount.Equal(got.Amount), "amount changed in transit: %s -> %s", sent.Amount, got.Amount)
	assert.Equal(t, sent.TransactionDate, got.TransactionDate.UTC())
}

func TestProcessedPaymentIsAcknowledged(t *testing.T) {
	h := newHarness(t, nil)

	require.NoError(t, h.queue.Enqueue(context.Background(), notification("VPAY-QUEUE-000002")))
	waitFor(t, func() bool { return h.processor.count() == 1 }, "the notification to be processed")

	// An acknowledged message leaves the pending list; one that stayed there
	// would be redelivered for ever.
	waitFor(t, func() bool {
		pending, err := h.queue.client.XPending(context.Background(), h.queue.stream, h.queue.group).Result()
		return err == nil && pending.Count == 0
	}, "the message to be acknowledged")
}

// A transient failure — the database being down, say — must not lose the
// credit. The message stays pending and the reclaimer hands it back.
func TestTransientFailureIsRedelivered(t *testing.T) {
	h := newHarness(t, nil)
	h.processor.err = errors.New("database is unreachable")

	require.NoError(t, h.queue.Enqueue(context.Background(), notification("VPAY-QUEUE-000003")))
	waitFor(t, func() bool { return h.processor.count() >= 2 }, "the notification to be retried")

	// Clearing the fault lets the retry succeed and the message drain.
	h.processor.mu.Lock()
	h.processor.err = nil
	h.processor.mu.Unlock()

	waitFor(t, func() bool {
		pending, err := h.queue.client.XPending(context.Background(), h.queue.stream, h.queue.group).Result()
		return err == nil && pending.Count == 0
	}, "the retried message to be acknowledged")
}

// A payload the domain rejects will be rejected identically for ever, so it is
// retired to the dead-letter stream instead of occupying a worker.
func TestPermanentFailureIsDeadLettered(t *testing.T) {
	h := newHarness(t, nil)
	h.processor.err = pkgerrors.BadRequest("customer_id is required")

	require.NoError(t, h.queue.Enqueue(context.Background(), notification("VPAY-QUEUE-000004")))

	waitFor(t, func() bool {
		dead, err := h.queue.client.XLen(context.Background(), h.queue.deadStream).Result()
		return err == nil && dead == 1
	}, "the message to be dead-lettered")

	// Exactly once: a dead-lettered message is acknowledged, not retried.
	assert.Equal(t, 1, h.processor.count())
}

func TestEnqueueFailsWhenTheBrokerIsGone(t *testing.T) {
	h := newHarness(t, nil)
	h.server.Close()

	err := h.queue.Enqueue(context.Background(), notification("VPAY-QUEUE-000005"))
	require.Error(t, err, "the caller must learn the credit was not queued, so it can apply it inline")
}

func TestNoQueueWhenRedisIsNotConfigured(t *testing.T) {
	cfg := &config.Config{}
	cfg.Queue.Enabled = true // enabled, but with nowhere to connect

	q, err := New(cfg, observability.NewMetrics(), zap.NewNop())
	require.NoError(t, err)
	assert.Nil(t, q, "without a broker the service applies payments inline")
}

// fxtestLifecycle runs lifecycle hooks without building a whole fx app.
type fxtestLifecycle struct {
	hooks []fx.Hook
}

func (l *fxtestLifecycle) Append(h fx.Hook) { l.hooks = append(l.hooks, h) }

func (l *fxtestLifecycle) start(ctx context.Context) error {
	for _, h := range l.hooks {
		if h.OnStart == nil {
			continue
		}
		if err := h.OnStart(ctx); err != nil {
			return err
		}
	}
	return nil
}

func (l *fxtestLifecycle) stop(ctx context.Context) error {
	for i := len(l.hooks) - 1; i >= 0; i-- {
		if l.hooks[i].OnStop == nil {
			continue
		}
		if err := l.hooks[i].OnStop(ctx); err != nil {
			return err
		}
	}
	return nil
}
