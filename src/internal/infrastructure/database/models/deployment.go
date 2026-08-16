package models

import (
	"time"

	"asset-repayment-service/internal/domain/deployment"
	"asset-repayment-service/pkg/money"
	"github.com/google/uuid"
)

// DeploymentModel is the row shape for the deployments table.
type DeploymentModel struct {
	ID                   int64        `db:"id"`
	UUID                 uuid.UUID    `db:"uuid"`
	CustomerID           string       `db:"customer_id"`
	AssetID              string       `db:"asset_id"`
	AssetType            string       `db:"asset_type"`
	VirtualAccountNumber *string      `db:"virtual_account_number"`
	Currency             string       `db:"currency"`
	Principal            money.Amount `db:"principal"`
	TermWeeks            int          `db:"term_weeks"`
	AmountPaid           money.Amount `db:"amount_paid"`
	PaymentCount         int64        `db:"payment_count"`
	Status               string       `db:"status"`
	StartDate            time.Time    `db:"start_date"`
	LastPaymentAt        *time.Time   `db:"last_payment_at"`
	CompletedAt          *time.Time   `db:"completed_at"`
	CreatedAt            time.Time    `db:"created_at"`
	UpdatedAt            time.Time    `db:"updated_at"`
}

// ToDomain maps the row to the domain entity.
func (m DeploymentModel) ToDomain() *deployment.Deployment {
	entity := &deployment.Deployment{
		ID:            m.ID,
		UUID:          m.UUID,
		CustomerID:    m.CustomerID,
		AssetID:       m.AssetID,
		AssetType:     m.AssetType,
		Currency:      money.Lookup(m.Currency),
		Principal:     m.Principal,
		TermWeeks:     m.TermWeeks,
		AmountPaid:    m.AmountPaid,
		PaymentCount:  m.PaymentCount,
		Status:        m.Status,
		StartDate:     m.StartDate.UTC(),
		LastPaymentAt: m.LastPaymentAt,
		CompletedAt:   m.CompletedAt,
		CreatedAt:     m.CreatedAt,
		UpdatedAt:     m.UpdatedAt,
	}
	if m.VirtualAccountNumber != nil {
		entity.VirtualAccountNumber = *m.VirtualAccountNumber
	}
	return entity
}

// DeploymentColumns is the canonical projection, kept in one place so the
// several queries that return a deployment cannot drift apart.
const DeploymentColumns = `id, uuid, customer_id, asset_id, asset_type, virtual_account_number,
	currency, principal, term_weeks, amount_paid, payment_count, status, start_date,
	last_payment_at, completed_at, created_at, updated_at`
