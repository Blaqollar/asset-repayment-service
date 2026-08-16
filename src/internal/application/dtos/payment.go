package dtos

import (
	"time"

	"asset-repayment-service/internal/domain/deployment"
	"asset-repayment-service/internal/domain/payment"
	"asset-repayment-service/pkg/money"
)

// ApplyPaymentRequest is the provider's notification payload
type ApplyPaymentRequest struct {
	CustomerID           string `json:"customer_id"`
	PaymentStatus        string `json:"payment_status"`
	TransactionAmount    string `json:"transaction_amount"`
	TransactionDate      string `json:"transaction_date"`
	TransactionReference string `json:"transaction_reference"`
	Currency             string `json:"currency,omitempty"`
}

// ToNotification maps the transport payload onto the domain type.
func (r ApplyPaymentRequest) ToNotification() payment.Notification {
	return payment.Notification(r)
}

// ApplyPaymentResponse is returned for every notification.
type ApplyPaymentResponse struct {
	Outcome   string       `json:"outcome"`
	Duplicate bool         `json:"duplicate"`
	Payment   *PaymentDTO  `json:"payment,omitempty"`
	Position  *PositionDTO `json:"position,omitempty"`
}

// PaymentDTO is a single ledger entry.
type PaymentDTO struct {
	Reference       string    `json:"transaction_reference"`
	CustomerID      string    `json:"customer_id"`
	Currency        string    `json:"currency"`
	Amount          string    `json:"amount"`
	AmountApplied   string    `json:"amount_applied,omitempty"`
	Excess          string    `json:"excess,omitempty"`
	BalanceBefore   string    `json:"balance_before,omitempty"`
	BalanceAfter    string    `json:"balance_after,omitempty"`
	Outcome         string    `json:"outcome"`
	ProviderStatus  string    `json:"provider_status"`
	TransactionDate time.Time `json:"transaction_date"`
	ProcessedAt     time.Time `json:"processed_at,omitempty"`
}

// NewPaymentDTO maps a ledger entry to its transport shape.
func NewPaymentDTO(p *payment.Payment) *PaymentDTO {
	if p == nil {
		return nil
	}
	if p.Outcome == payment.OutcomeQueued {
		return &PaymentDTO{
			Reference:       p.TransactionReference,
			CustomerID:      p.CustomerID,
			Currency:        p.Currency.Code,
			Amount:          money.Format(p.Amount, p.Currency),
			Outcome:         p.Outcome,
			ProviderStatus:  p.ProviderStatus,
			TransactionDate: p.TransactionDate,
		}
	}
	return &PaymentDTO{
		Reference:       p.TransactionReference,
		CustomerID:      p.CustomerID,
		Currency:        p.Currency.Code,
		Amount:          money.Format(p.Amount, p.Currency),
		AmountApplied:   money.Format(p.Applied, p.Currency),
		Excess:          money.Format(p.Excess, p.Currency),
		BalanceBefore:   money.Format(p.BalanceBefore, p.Currency),
		BalanceAfter:    money.Format(p.BalanceAfter, p.Currency),
		Outcome:         p.Outcome,
		ProviderStatus:  p.ProviderStatus,
		TransactionDate: p.TransactionDate,
		ProcessedAt:     p.CreatedAt,
	}
}

// PositionDTO is the customer's standing after the payment.
type PositionDTO struct {
	CustomerID              string     `json:"customer_id"`
	DeploymentID            string     `json:"deployment_id"`
	AssetID                 string     `json:"asset_id"`
	Status                  string     `json:"status"`
	ScheduleStatus          string     `json:"schedule_status"`
	Currency                string     `json:"currency"`
	Principal               string     `json:"principal"`
	TotalPaid               string     `json:"total_paid"`
	Outstanding             string     `json:"outstanding"`
	Excess                  string     `json:"excess_credit"`
	WeeklyDue               string     `json:"weekly_due"`
	ExpectedPaidToDate      string     `json:"expected_paid_to_date"`
	Arrears                 string     `json:"arrears"`
	TermWeeks               int        `json:"term_weeks"`
	WeeksElapsed            int        `json:"weeks_elapsed"`
	WeeksOfCoverage         float64    `json:"weeks_of_coverage"`
	PercentRepaid           float64    `json:"percent_repaid"`
	PaymentCount            int64      `json:"payment_count"`
	StartDate               string     `json:"start_date"`
	ExpectedCompletionDate  string     `json:"expected_completion_date"`
	ProjectedCompletionDate *string    `json:"projected_completion_date"`
	LastPaymentAt           *time.Time `json:"last_payment_at"`
	AsOf                    time.Time  `json:"as_of"`
}

const dateOnly = "2006-01-02"

// NewPositionDTO maps a domain position to its transport shape.
func NewPositionDTO(p *deployment.Position) *PositionDTO {
	if p == nil {
		return nil
	}

	dto := &PositionDTO{
		CustomerID:             p.CustomerID,
		DeploymentID:           p.DeploymentUUID.String(),
		AssetID:                p.AssetID,
		Status:                 p.Status,
		ScheduleStatus:         p.ScheduleStatus,
		Currency:               p.Currency.Code,
		Principal:              money.Format(p.Principal, p.Currency),
		TotalPaid:              money.Format(p.AmountPaid, p.Currency),
		Outstanding:            money.Format(p.Outstanding, p.Currency),
		Excess:                 money.Format(p.Excess, p.Currency),
		WeeklyDue:              money.Format(p.WeeklyDue, p.Currency),
		ExpectedPaidToDate:     money.Format(p.ExpectedPaidToDate, p.Currency),
		Arrears:                money.Format(p.Arrears, p.Currency),
		TermWeeks:              p.TermWeeks,
		WeeksElapsed:           p.WeeksElapsed,
		WeeksOfCoverage:        p.WeeksOfCoverage,
		PercentRepaid:          p.PercentRepaid,
		PaymentCount:           p.PaymentCount,
		StartDate:              p.StartDate.Format(dateOnly),
		ExpectedCompletionDate: p.ExpectedCompletionDate.Format(dateOnly),
		LastPaymentAt:          p.LastPaymentAt,
		AsOf:                   p.AsOf,
	}

	if p.ProjectedCompletionDate != nil {
		projected := p.ProjectedCompletionDate.Format(dateOnly)
		dto.ProjectedCompletionDate = &projected
	}
	return dto
}
