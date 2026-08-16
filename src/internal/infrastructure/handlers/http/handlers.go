package httpx

import (
	"asset-repayment-service/internal/application/usecases"
	"asset-repayment-service/internal/infrastructure/cache"
	"github.com/jmoiron/sqlx"
	"go.uber.org/fx"
	"go.uber.org/zap"
)

// Handlers holds the HTTP entry points and the dependencies they share.
type Handlers struct {
	applyPayment     *usecases.ApplyPaymentUsecase
	getPosition      *usecases.GetPositionUsecase
	createDeployment *usecases.CreateDeploymentUsecase

	db     *sqlx.DB
	cache  *cache.Cache
	logger *zap.Logger
}

// HandlerParams groups the dependencies
type HandlerParams struct {
	fx.In

	ApplyPayment     *usecases.ApplyPaymentUsecase
	GetPosition      *usecases.GetPositionUsecase
	CreateDeployment *usecases.CreateDeploymentUsecase

	DB     *sqlx.DB
	Cache  *cache.Cache
	Logger *zap.Logger
}

// NewHandlers creates the HTTP entry points.
func NewHandlers(params HandlerParams) *Handlers {
	return &Handlers{
		applyPayment:     params.ApplyPayment,
		getPosition:      params.GetPosition,
		createDeployment: params.CreateDeployment,
		db:               params.DB,
		cache:            params.Cache,
		logger:           params.Logger.With(zap.String("module", "http")),
	}
}

// Module wires the handlers and the server; the mux comes from pkg/router.
var Module = fx.Module("http", fx.Options(
	fx.Provide(NewHandlers, NewServer),
	fx.Invoke(func(*Server) {}),
))
