package models

import (
	"encoding/json"
	"time"

	"asset-repayment-service/internal/domain/payment"
	"asset-repayment-service/pkg/money"
	"github.com/google/uuid"
)

// PaymentModel is the row shape for the payments ledger.
type PaymentModel struct {
	UUID                 uuid.UUID       `db:"uuid"`
	TransactionReference string          `db:"transaction_reference"`
	CustomerID           string          `db:"customer_id"`
	DeploymentID         *int64          `db:"deployment_id"`
	Currency             string          `db:"currency"`
	Amount               money.Amount    `db:"amount"`
	AppliedAmount        money.Amount    `db:"applied_amount"`
	ExcessAmount         money.Amount    `db:"excess_amount"`
	BalanceBefore        money.Amount    `db:"balance_before"`
	BalanceAfter         money.Amount    `db:"balance_after"`
	Outcome              string          `db:"outcome"`
	ProviderStatus       string          `db:"provider_status"`
	TransactionDate      time.Time       `db:"transaction_date"`
	ReceivedAt           time.Time       `db:"received_at"`
	CreatedAt            time.Time       `db:"created_at"`
	RawPayload           json.RawMessage `db:"raw_payload"`
}

// ToDomain maps the row to the domain entity.
func (m PaymentModel) ToDomain() *payment.Payment {
	return &payment.Payment{
		UUID:                 m.UUID,
		TransactionReference: m.TransactionReference,
		CustomerID:           m.CustomerID,
		DeploymentID:         m.DeploymentID,
		Currency:             money.Lookup(m.Currency),
		Amount:               m.Amount,
		Applied:              m.AppliedAmount,
		Excess:               m.ExcessAmount,
		BalanceBefore:        m.BalanceBefore,
		BalanceAfter:         m.BalanceAfter,
		Outcome:              m.Outcome,
		ProviderStatus:       m.ProviderStatus,
		TransactionDate:      m.TransactionDate.UTC(),
		ReceivedAt:           m.ReceivedAt.UTC(),
		CreatedAt:            m.CreatedAt.UTC(),
		RawPayload:           m.RawPayload,
	}
}

// PaymentColumns is the canonical projection for ledger reads.
const PaymentColumns = `uuid, transaction_reference, customer_id, deployment_id, currency,
	amount, applied_amount, excess_amount, balance_before, balance_after, outcome,
	provider_status, transaction_date, received_at, created_at, raw_payload`

// ApplyRowModel is the single row returned by the atomic apply statement.
type ApplyRowModel struct {
	PaymentUUID      uuid.UUID    `db:"payment_uuid"`
	AppliedAmount    money.Amount `db:"applied_amount"`
	ExcessAmount     money.Amount `db:"excess_amount"`
	BalanceBefore    money.Amount `db:"balance_before"`
	BalanceAfter     money.Amount `db:"balance_after"`
	PaymentCreatedAt time.Time    `db:"payment_created_at"`

	DeploymentModel
}
