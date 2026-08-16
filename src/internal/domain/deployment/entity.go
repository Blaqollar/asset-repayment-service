package deployment

import (
	"math"
	"time"

	"asset-repayment-service/pkg/money"
	"github.com/google/uuid"
)

// Deployment is an asset handed to a customer, with a repayment schedule and a record of how much has been paid. 
type Deployment struct {
	ID                   int64     // internal surrogate key, never exposed
	UUID                 uuid.UUID // public identifier
	CustomerID           string    // e.g. "GIG00001"
	AssetID              string
	AssetType            string
	VirtualAccountNumber string
	Currency  money.Currency
	Principal money.Amount
	TermWeeks int
	AmountPaid   money.Amount
	PaymentCount int64
	Status        string
	StartDate     time.Time
	LastPaymentAt *time.Time
	CompletedAt   *time.Time
	CreatedAt     time.Time
	UpdatedAt     time.Time
}

// Deployment lifecycle statuses.
const (
	StatusActive     = "active"     // repaying, on or ahead of schedule
	StatusDelinquent = "delinquent" // repaying, materially behind schedule
	StatusCompleted  = "completed"  // fully repaid; asset ownership transfers
	StatusWrittenOff = "written_off"
)

// Repayment schedule health, derived (never stored) from the position.
const (
	ScheduleAhead   = "ahead"
	ScheduleOnTrack = "on_track"
	ScheduleBehind  = "behind"
	ScheduleSettled = "settled"
)

// IsOpen reports whether the deployment still accrues repayments.
func (d *Deployment) IsOpen() bool {
	return d.Status == StatusActive || d.Status == StatusDelinquent
}

// Outstanding is what is still owed, floored at zero — overpayment is carried
// as Excess rather than as a negative balance.
func (d *Deployment) Outstanding() money.Amount {
	return money.NonNegative(d.Principal.Sub(d.AmountPaid))
}

// Excess is the amount paid beyond the principal, held as customer credit.
func (d *Deployment) Excess() money.Amount {
	return money.NonNegative(d.AmountPaid.Sub(d.Principal))
}

// WeeklyDue is the contractual weekly repayment
func (d *Deployment) WeeklyDue() money.Amount {
	return money.Prorate(d.Principal, 1, int64(d.TermWeeks), d.Currency)
}

// ExpectedCompletionDate is the contractual end of term.
func (d *Deployment) ExpectedCompletionDate() time.Time {
	return d.StartDate.AddDate(0, 0, d.TermWeeks*7)
}

// WeeksElapsed counts completed weeks since the start date, clamped to the term.
func (d *Deployment) WeeksElapsed(asOf time.Time) int {
	if asOf.Before(d.StartDate) {
		return 0
	}
	weeks := int(asOf.Sub(d.StartDate).Hours() / (24 * 7))
	if weeks > d.TermWeeks {
		weeks = d.TermWeeks
	}
	return weeks
}

// ExpectedPaidToDate is the pro-rata amount that should have been repaid
func (d *Deployment) ExpectedPaidToDate(asOf time.Time) money.Amount {
	if d.TermWeeks <= 0 {
		return d.Principal
	}
	return money.Prorate(d.Principal, int64(d.WeeksElapsed(asOf)), int64(d.TermWeeks), d.Currency)
}

// Arrears is how far behind schedule the customer is, floored at zero.
func (d *Deployment) Arrears(asOf time.Time) money.Amount {
	if !d.IsOpen() {
		return money.Zero
	}
	return money.NonNegative(d.ExpectedPaidToDate(asOf).Sub(d.AmountPaid))
}

// AheadBy is how far ahead of schedule the customer is, floored at zero.
func (d *Deployment) AheadBy(asOf time.Time) money.Amount {
	return money.NonNegative(d.creditedToSchedule(asOf))
}

// creditedToSchedule is the signed distance from schedule. 
func (d *Deployment) creditedToSchedule(asOf time.Time) money.Amount {
	return money.Min(d.AmountPaid, d.Principal).Sub(d.ExpectedPaidToDate(asOf))
}

// WeeksOfCoverage is the schedule position in weeks
func (d *Deployment) WeeksOfCoverage(asOf time.Time) float64 {
	weekly := d.WeeklyDue()
	if !weekly.IsPositive() {
		return 0
	}
	return round2(d.creditedToSchedule(asOf).Div(weekly).InexactFloat64())
}

// ScheduleStatus classifies repayment health. The one-instalment tolerance
// keeps a customer who paid a day late out of the delinquency queue.
func (d *Deployment) ScheduleStatus(asOf time.Time) string {
	if !d.IsOpen() || d.Outstanding().IsZero() {
		return ScheduleSettled
	}
	if d.Arrears(asOf).GreaterThan(d.WeeklyDue()) {
		return ScheduleBehind
	}
	if d.AheadBy(asOf).IsPositive() {
		return ScheduleAhead
	}
	return ScheduleOnTrack
}

// PercentRepaid is completion as a percentage of principal, capped at 100.
func (d *Deployment) PercentRepaid() float64 {
	if !d.Principal.IsPositive() {
		return 100
	}
	repaid := money.Min(d.AmountPaid, d.Principal)
	return round2(repaid.Div(d.Principal).InexactFloat64() * 100)
}

// ProjectedCompletionDate extrapolates the observed run-rate to the date the
// principal clears. 
func (d *Deployment) ProjectedCompletionDate(asOf time.Time) *time.Time {
	weeks := d.WeeksElapsed(asOf)
	if d.Outstanding().IsZero() || !d.IsOpen() || weeks < 1 || !d.AmountPaid.IsPositive() {
		return nil
	}

	ratePerWeek := d.AmountPaid.Div(money.FromInt(int64(weeks)))
	weeksRemaining := math.Ceil(d.Outstanding().Div(ratePerWeek).InexactFloat64())

	// A run-rate this low makes the projection meaningless.
	if math.IsInf(weeksRemaining, 0) || weeksRemaining > float64(10*d.TermWeeks) {
		return nil
	}

	projected := asOf.AddDate(0, 0, int(weeksRemaining)*7)
	return &projected
}

// SplitPayment divides a credit into the part that reduces the balance and the
// part that becomes customer credit.
func SplitPayment(principal, paidBefore, amount money.Amount) (applied, excess money.Amount) {
	room := money.NonNegative(principal.Sub(paidBefore))
	applied = money.Min(amount, room)
	return applied, amount.Sub(applied)
}

// Position is the customer's complete standing, returned with every applied
// payment so no follow-up read is needed.
type Position struct {
	CustomerID     string
	DeploymentUUID uuid.UUID
	AssetID        string
	Status         string
	ScheduleStatus string

	Currency           money.Currency
	Principal          money.Amount
	AmountPaid         money.Amount
	Outstanding        money.Amount
	Excess             money.Amount
	WeeklyDue          money.Amount
	ExpectedPaidToDate money.Amount
	Arrears            money.Amount

	TermWeeks               int
	WeeksElapsed            int
	WeeksOfCoverage         float64
	PercentRepaid           float64
	PaymentCount            int64
	StartDate               time.Time
	ExpectedCompletionDate  time.Time
	ProjectedCompletionDate *time.Time
	LastPaymentAt           *time.Time
	AsOf                    time.Time
}

// PositionAt materialises the full position as of a point in time.
func (d *Deployment) PositionAt(asOf time.Time) Position {
	return Position{
		CustomerID:     d.CustomerID,
		DeploymentUUID: d.UUID,
		AssetID:        d.AssetID,
		Status:         d.Status,
		ScheduleStatus: d.ScheduleStatus(asOf),

		Currency:           d.Currency,
		Principal:          d.Principal,
		AmountPaid:         d.AmountPaid,
		Outstanding:        d.Outstanding(),
		Excess:             d.Excess(),
		WeeklyDue:          d.WeeklyDue(),
		ExpectedPaidToDate: d.ExpectedPaidToDate(asOf),
		Arrears:            d.Arrears(asOf),

		TermWeeks:               d.TermWeeks,
		WeeksElapsed:            d.WeeksElapsed(asOf),
		WeeksOfCoverage:         d.WeeksOfCoverage(asOf),
		PercentRepaid:           d.PercentRepaid(),
		PaymentCount:            d.PaymentCount,
		StartDate:               d.StartDate,
		ExpectedCompletionDate:  d.ExpectedCompletionDate(),
		ProjectedCompletionDate: d.ProjectedCompletionDate(asOf),
		LastPaymentAt:           d.LastPaymentAt,
		AsOf:                    asOf,
	}
}

func round2(v float64) float64 { return math.Round(v*100) / 100 }
