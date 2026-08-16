package repositories

import "go.uber.org/fx"

// Module registers the repositories.
var Module = fx.Module("repositories",
	fx.Provide(
		NewDeploymentRepository,
		NewPaymentRepository,
	),
)
