package observability

import (
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/collectors"
	"go.uber.org/fx"
)

// Metrics is the instrumentation surface. 
type Metrics struct {
	registry *prometheus.Registry

	PaymentsTotal    *prometheus.CounterVec
	PaymentDuration  *prometheus.HistogramVec
	AmountApplied    prometheus.Counter
	PaymentsInflight prometheus.Gauge
	PaymentsShed     prometheus.Counter

	HTTPRequests *prometheus.CounterVec
	HTTPDuration *prometheus.HistogramVec

	// Queue health. Depth is the number to alert on.
	QueueEnqueued        prometheus.Counter
	QueueEnqueueFailures prometheus.Counter
	QueueRetries         prometheus.Counter
	QueueReclaimed       prometheus.Counter
	QueueDeadLettered    prometheus.Counter
	QueueDepth           prometheus.Gauge
	QueuePending         prometheus.Gauge
}

// NewMetrics registers collectors on a private registry, so /metrics carries
// only this service's series plus the Go runtime.
func NewMetrics() *Metrics {
	registry := prometheus.NewRegistry()
	registry.MustRegister(
		collectors.NewGoCollector(),
		collectors.NewProcessCollector(collectors.ProcessCollectorOpts{}),
	)

	m := &Metrics{
		registry: registry,

		PaymentsTotal: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "payment_notifications_total",
			Help: "Inbound payment notifications by processing outcome.",
		}, []string{"outcome"}),

		PaymentDuration: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Name: "payment_apply_duration_seconds",
			Help: "End-to-end time to apply a payment notification.",
			// Tightened around the single-digit milliseconds a healthy apply
			// takes; the defaults start at 5ms and would hide the signal.
			Buckets: []float64{0.001, 0.0025, 0.005, 0.01, 0.025, 0.05, 0.1, 0.25, 0.5, 1, 2.5},
		}, []string{"outcome"}),

		AmountApplied: prometheus.NewCounter(prometheus.CounterOpts{
			Name: "payment_applied_amount_total",
			Help: "Cumulative amount applied to outstanding balances, in whole currency units.",
		}),

		PaymentsInflight: prometheus.NewGauge(prometheus.GaugeOpts{
			Name: "payment_inflight_requests",
			Help: "Payment notifications currently being processed.",
		}),

		PaymentsShed: prometheus.NewCounter(prometheus.CounterOpts{
			Name: "payment_shed_total",
			Help: "Payment notifications rejected with 429 because the service was at capacity.",
		}),

		HTTPRequests: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "http_requests_total",
			Help: "HTTP requests by route, method and status class.",
		}, []string{"route", "method", "status"}),

		HTTPDuration: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Name:    "http_request_duration_seconds",
			Help:    "HTTP request latency by route.",
			Buckets: []float64{0.001, 0.005, 0.01, 0.025, 0.05, 0.1, 0.25, 0.5, 1, 2.5, 5},
		}, []string{"route"}),

		QueueEnqueued: prometheus.NewCounter(prometheus.CounterOpts{
			Name: "payment_queue_enqueued_total",
			Help: "Notifications accepted and handed to the queue.",
		}),
		QueueEnqueueFailures: prometheus.NewCounter(prometheus.CounterOpts{
			Name: "payment_queue_enqueue_failures_total",
			Help: "Enqueue attempts that failed and fell back to inline application.",
		}),
		QueueRetries: prometheus.NewCounter(prometheus.CounterOpts{
			Name: "payment_queue_retries_total",
			Help: "Queued payments left pending for redelivery after a transient failure.",
		}),
		QueueReclaimed: prometheus.NewCounter(prometheus.CounterOpts{
			Name: "payment_queue_reclaimed_total",
			Help: "Messages reclaimed from a consumer that stopped acknowledging.",
		}),
		QueueDeadLettered: prometheus.NewCounter(prometheus.CounterOpts{
			Name: "payment_queue_dead_lettered_total",
			Help: "Payments moved to the dead-letter stream for manual reconciliation.",
		}),
		QueueDepth: prometheus.NewGauge(prometheus.GaugeOpts{
			Name: "payment_queue_depth",
			Help: "Notifications currently on the inbound stream.",
		}),
		QueuePending: prometheus.NewGauge(prometheus.GaugeOpts{
			Name: "payment_queue_pending",
			Help: "Messages delivered to a worker but not yet acknowledged.",
		}),
	}

	registry.MustRegister(
		m.PaymentsTotal,
		m.PaymentDuration,
		m.AmountApplied,
		m.PaymentsInflight,
		m.PaymentsShed,
		m.HTTPRequests,
		m.HTTPDuration,
		m.QueueEnqueued,
		m.QueueEnqueueFailures,
		m.QueueRetries,
		m.QueueReclaimed,
		m.QueueDeadLettered,
		m.QueueDepth,
		m.QueuePending,
	)
	return m
}

// RecordPayment counts one notification and its latency together, so a caller
// cannot update one and forget the other.
func (m *Metrics) RecordPayment(outcome string, elapsed time.Duration) {
	m.PaymentsTotal.WithLabelValues(outcome).Inc()
	m.PaymentDuration.WithLabelValues(outcome).Observe(elapsed.Seconds())
}

// RecordHTTP counts one served request and its latency.
func (m *Metrics) RecordHTTP(route, method, statusClass string, elapsed time.Duration) {
	m.HTTPRequests.WithLabelValues(route, method, statusClass).Inc()
	m.HTTPDuration.WithLabelValues(route).Observe(elapsed.Seconds())
}

// Registry exposes the collector registry for the /metrics handler.
func (m *Metrics) Registry() *prometheus.Registry { return m.registry }

// Module provides metrics to the FX graph.
var Module = fx.Module("observability", fx.Provide(NewMetrics))
