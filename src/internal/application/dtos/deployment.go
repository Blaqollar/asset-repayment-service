package dtos

import (
	"time"

	"asset-repayment-service/internal/domain/deployment"
	"asset-repayment-service/pkg/money"
)

// CreateDeploymentRequest registers an asset handed to an entrepreneur.
type CreateDeploymentRequest struct {
	CustomerID           string `json:"customer_id"`
	AssetID              string `json:"asset_id"`
	AssetType            string `json:"asset_type"`
	VirtualAccountNumber string `json:"virtual_account_number"`
	Currency             string `json:"currency"`
	Principal            string `json:"principal"`
	TermWeeks            int    `json:"term_weeks"`
	StartDate            string `json:"start_date"`
}

// DeploymentResponse is the transport shape of a deployment.
type DeploymentResponse struct {
	DeploymentID         string       `json:"deployment_id"`
	CustomerID           string       `json:"customer_id"`
	AssetID              string       `json:"asset_id"`
	AssetType            string       `json:"asset_type"`
	VirtualAccountNumber string       `json:"virtual_account_number,omitempty"`
	Currency             string       `json:"currency"`
	Principal            string       `json:"principal"`
	TermWeeks            int          `json:"term_weeks"`
	WeeklyDue            string       `json:"weekly_due"`
	Status               string       `json:"status"`
	StartDate            string       `json:"start_date"`
	ExpectedCompletion   string       `json:"expected_completion_date"`
	CreatedAt            time.Time    `json:"created_at"`
	Position             *PositionDTO `json:"position,omitempty"`
}

// NewDeploymentResponse maps a deployment to its transport shape.
func NewDeploymentResponse(d *deployment.Deployment, asOf time.Time) *DeploymentResponse {
	if d == nil {
		return nil
	}

	position := d.PositionAt(asOf)
	return &DeploymentResponse{
		DeploymentID:         d.UUID.String(),
		CustomerID:           d.CustomerID,
		AssetID:              d.AssetID,
		AssetType:            d.AssetType,
		VirtualAccountNumber: d.VirtualAccountNumber,
		Currency:             d.Currency.Code,
		Principal:            money.Format(d.Principal, d.Currency),
		TermWeeks:            d.TermWeeks,
		WeeklyDue:            money.Format(d.WeeklyDue(), d.Currency),
		Status:               d.Status,
		StartDate:            d.StartDate.Format(dateOnly),
		ExpectedCompletion:   d.ExpectedCompletionDate().Format(dateOnly),
		CreatedAt:            d.CreatedAt,
		Position:             NewPositionDTO(&position),
	}
}
