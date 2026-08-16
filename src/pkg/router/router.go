package router

import (
	"net/http"

	"asset-repayment-service/internal/config"
	httpx "asset-repayment-service/internal/infrastructure/handlers/http"
	"asset-repayment-service/internal/infrastructure/observability"
	"asset-repayment-service/pkg/middleware"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"go.uber.org/fx"
	"go.uber.org/zap"
)

// Setup builds the mux.
func Setup(
	handlers *httpx.Handlers,
	cfg *config.Config,
	metrics *observability.Metrics,
	log *zap.Logger,
) *http.ServeMux {
	logger := log.With(zap.String("module", "http"))

	// base returns the middleware every route shares
	base := func(route string, extra ...middleware.Middleware) []middleware.Middleware {
		return append([]middleware.Middleware{
			middleware.Recovery(logger),
			middleware.RequestID(logger),
			middleware.Observability(route, metrics),
			middleware.Body(cfg.HTTP.MaxBodyBytes),
		}, extra...)
	}

	mux := http.NewServeMux()

	mux.Handle("POST /api/v1/payments", middleware.Chain(
		http.HandlerFunc(handlers.ApplyPayment),
		base("apply_payment",
			middleware.Signature(cfg, logger),
			middleware.LoadShedding(cfg.HTTP.MaxInflightPayments, metrics),
			middleware.Timeout(cfg.HTTP.RequestTimeout),
		)...,
	))

	mux.Handle("POST /api/v1/deployments", middleware.Chain(
		http.HandlerFunc(handlers.CreateDeployment),
		base("create_deployment")...,
	))

	mux.Handle("GET /api/v1/customers/{customer_id}/position", middleware.Chain(
		http.HandlerFunc(handlers.GetPosition),
		base("get_position")...,
	))

	mux.HandleFunc("GET /healthz", handlers.Healthz)
	mux.HandleFunc("GET /readyz", handlers.Readyz)

	if cfg.Observability.MetricsEnabled {
		mux.Handle("GET /metrics", promhttp.HandlerFor(
			metrics.Registry(),
			promhttp.HandlerOpts{Registry: metrics.Registry()},
		))
	}

	return mux
}

// Module provides the mux to the FX graph.
var Module = fx.Module("router", fx.Provide(Setup))
