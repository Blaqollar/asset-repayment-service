package usecases

import "go.uber.org/fx"

// Module registers every usecase. Usecases depend on domain services and never
// on repositories directly (for separation of concerns). Usecases are orchestrators, not business logic.
var Module = fx.Module("usecases",
	fx.Provide(
		NewApplyPaymentUsecase,
		NewGetPositionUsecase,
		NewCreateDeploymentUsecase,
	),
)
