package bootstrap

import (
	"asset-repayment-service/internal/application/usecases"
	"asset-repayment-service/internal/config"
	"asset-repayment-service/internal/domain"
	"asset-repayment-service/internal/infrastructure/cache"
	"asset-repayment-service/internal/infrastructure/database"
	"asset-repayment-service/internal/infrastructure/database/repositories"
	httpx "asset-repayment-service/internal/infrastructure/handlers/http"
	"asset-repayment-service/internal/infrastructure/observability"
	"asset-repayment-service/internal/infrastructure/queue"
	"asset-repayment-service/pkg/logger"
	"asset-repayment-service/pkg/router"
	"go.uber.org/fx"
	"go.uber.org/fx/fxevent"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
)

// NewApp is the whole object graph: it declares what exists, nothing more.
func NewApp() fx.Option {
	return fx.Options(

		fx.WithLogger(func(log *zap.Logger) fxevent.Logger {
			fxLogger := &fxevent.ZapLogger{Logger: log.Named("fx")}
			fxLogger.UseLogLevel(zapcore.DebugLevel)
			return fxLogger
		}),

		// Core
		fx.Provide(logger.GetLogger),
		config.Module,

		// Infrastructure
		observability.Module,
		database.Module,
		cache.Module,
		queue.Module,
		repositories.Module,

		// Domain
		domain.Module,

		// Application
		usecases.Module,

		// Transport
		router.Module,
		httpx.Module,
	)
}
