package deployment

import (
	"context"
	"time"

	"asset-repayment-service/pkg/money"
	"github.com/google/uuid"
)

// CreateInput describes a new asset deployment.
type CreateInput struct {
	CustomerID           string
	AssetID              string
	AssetType            string
	VirtualAccountNumber string
	Currency             money.Currency
	Principal            money.Amount
	TermWeeks            int
	StartDate            time.Time
}

// Repository defines deployment persistence. 
type Repository interface {
	Create(ctx context.Context, input CreateInput) (*Deployment, error)
	GetByCustomerID(ctx context.Context, customerID string) (*Deployment, error)
}

// Ref is the immutable subset needed to route a payment.
type Ref struct {
	ID         int64          `json:"id"`
	UUID       uuid.UUID      `json:"uuid"`
	CustomerID string         `json:"customer_id"`
	AssetID    string         `json:"asset_id"`
	Currency   money.Currency `json:"currency"`
	Principal  money.Amount   `json:"principal"`
	TermWeeks  int            `json:"term_weeks"`
	StartDate  time.Time      `json:"start_date"`
}

// ToRef projects the cacheable routing subset.
func (d *Deployment) ToRef() Ref {
	return Ref{
		ID:         d.ID,
		UUID:       d.UUID,
		CustomerID: d.CustomerID,
		AssetID:    d.AssetID,
		Currency:   d.Currency,
		Principal:  d.Principal,
		TermWeeks:  d.TermWeeks,
		StartDate:  d.StartDate,
	}
}

// RefCache is a best-effort lookaside for payment routing.
type RefCache interface {
	GetRef(ctx context.Context, customerID string) (*Ref, bool)
	SetRef(ctx context.Context, ref Ref)
	InvalidateRef(ctx context.Context, customerID string)
}
