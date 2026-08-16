package usecases

import (
	"context"
	"time"

	"asset-repayment-service/internal/application/dtos"
	"asset-repayment-service/internal/domain/deployment"
	pkgerrors "asset-repayment-service/pkg/errors"
)

// GetPositionUsecase answers "where does this customer stand right now".
type GetPositionUsecase struct {
	deployments deployment.Service
}

// NewGetPositionUsecase creates the position read usecase.
func NewGetPositionUsecase(deployments deployment.Service) *GetPositionUsecase {
	return &GetPositionUsecase{deployments: deployments}
}

// Execute returns the customer's current repayment position.
func (uc *GetPositionUsecase) Execute(ctx context.Context, customerID string) (*dtos.PositionDTO, error) {
	if customerID == "" {
		return nil, pkgerrors.BadRequest("customer_id is required")
	}

	position, err := uc.deployments.GetPosition(ctx, customerID, time.Now().UTC())
	if err != nil {
		return nil, err
	}
	return dtos.NewPositionDTO(position), nil
}
