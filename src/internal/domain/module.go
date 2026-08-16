package domain

import (
	"asset-repayment-service/internal/domain/deployment"
	"asset-repayment-service/internal/domain/payment"
	"go.uber.org/fx"
)

// Module registers the domain services so usecases can depend on them.
var Module = fx.Module("domain",
	fx.Provide(
		deployment.NewService,
		payment.NewService,
	),
)
