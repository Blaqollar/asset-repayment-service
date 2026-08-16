package usecases

import (
	"context"
	"fmt"
	"time"

	"asset-repayment-service/internal/application/dtos"
	"asset-repayment-service/internal/domain/deployment"
	pkgerrors "asset-repayment-service/pkg/errors"
	"asset-repayment-service/pkg/money"
	"github.com/google/uuid"
	"go.uber.org/zap"
)

// CreateDeploymentUsecase registers an asset
type CreateDeploymentUsecase struct {
	deployments deployment.Service
	logger      *zap.Logger
}

// NewCreateDeploymentUsecase creates the deployment registration usecase.
func NewCreateDeploymentUsecase(deployments deployment.Service, logger *zap.Logger) *CreateDeploymentUsecase {
	return &CreateDeploymentUsecase{
		deployments: deployments,
		logger:      logger.With(zap.String("usecase", "create_deployment")),
	}
}

// Execute registers the deployment. Currency, principal and term are optional;
// the domain fills them from the standard programme terms.
func (uc *CreateDeploymentUsecase) Execute(ctx context.Context, req *dtos.CreateDeploymentRequest) (*dtos.DeploymentResponse, error) {
	if req == nil {
		return nil, pkgerrors.BadRequest("request body is required")
	}

	input := deployment.CreateInput{
		CustomerID:           req.CustomerID,
		AssetID:              req.AssetID,
		AssetType:            req.AssetType,
		VirtualAccountNumber: req.VirtualAccountNumber,
		TermWeeks:            req.TermWeeks,
	}

	input.Currency = uc.deployments.Defaults().Currency
	if req.Currency != "" {
		input.Currency = money.Lookup(req.Currency)
	}
	if req.Principal != "" {
		parsed, err := money.ParsePositive(req.Principal, input.Currency)
		if err != nil {
			return nil, pkgerrors.ValidationFailed(map[string]any{"principal": err.Error()})
		}
		input.Principal = parsed
	}
	if req.StartDate != "" {
		parsed, err := time.Parse(dateOnly, req.StartDate)
		if err != nil {
			return nil, pkgerrors.ValidationFailed(map[string]any{
				"start_date": "must be a date in YYYY-MM-DD format",
			})
		}
		input.StartDate = parsed.UTC()
	}
	if input.AssetType == "" {
		input.AssetType = "mobility"
	}
	if input.AssetID == "" {
		input.AssetID = fmt.Sprintf("AST-%s", uuid.NewString()[:8])
	}

	created, err := uc.deployments.Create(ctx, input)
	if err != nil {
		return nil, err
	}

	uc.logger.Info("deployment registered",
		zap.String("customer_id", created.CustomerID),
		zap.String("deployment_id", created.UUID.String()),
		zap.String("principal", money.Format(created.Principal, created.Currency)),
		zap.String("currency", created.Currency.Code),
		zap.Int("term_weeks", created.TermWeeks),
	)

	return dtos.NewDeploymentResponse(created, time.Now().UTC()), nil
}

// dateOnly is the date format the API accepts and emits.
const dateOnly = "2006-01-02"
