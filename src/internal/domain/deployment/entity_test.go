package deployment

import (
	"testing"
	"time"

	"asset-repayment-service/pkg/money"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

var (
	start = time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)
	ngn   = money.Lookup("NGN")
)

// newDeployment builds the standard programme asset
func newDeployment(paid int64) *Deployment {
	return &Deployment{
		CustomerID: "GIG00001",
		Currency:   ngn,
		Principal:  money.FromInt(1_000_000),
		TermWeeks:  50,
		AmountPaid: money.FromInt(paid),
		Status:     StatusActive,
		StartDate:  start,
	}
}

func weeksIn(n int) time.Time { return start.AddDate(0, 0, n*7) }

// equalAmount compares at the currency's precision rather than by struct
// identity: 20000 and 20000.000000 are the same amount of money, and only one
// of them is what a NUMERIC column hands back.
func equalAmount(t *testing.T, expected string, actual money.Amount, msgAndArgs ...any) {
	t.Helper()
	assert.Equal(t, expected, money.Format(actual, ngn), msgAndArgs...)
}

func TestWeeklyDueIsPrincipalOverTerm(t *testing.T) {
	equalAmount(t, "20000.00", newDeployment(0).WeeklyDue())
}

func TestOutstandingNeverGoesNegative(t *testing.T) {
	d := newDeployment(1_200_000)
	equalAmount(t, "0.00", d.Outstanding())
	equalAmount(t, "200000.00", d.Excess())
}

func TestWeeksElapsedClampsToTerm(t *testing.T) {
	d := newDeployment(0)
	assert.Equal(t, 0, d.WeeksElapsed(start))
	assert.Equal(t, 0, d.WeeksElapsed(start.AddDate(0, 0, 6)))
	assert.Equal(t, 1, d.WeeksElapsed(weeksIn(1)))
	assert.Equal(t, 10, d.WeeksElapsed(weeksIn(10)))
	assert.Equal(t, 50, d.WeeksElapsed(weeksIn(80)), "cannot elapse past the term")
}

func TestWeeksElapsedIsZeroBeforeStart(t *testing.T) {
	assert.Equal(t, 0, newDeployment(0).WeeksElapsed(start.AddDate(0, 0, -30)))
}

func TestExpectedPaidToDateIsProRata(t *testing.T) {
	d := newDeployment(0)
	equalAmount(t, "0.00", d.ExpectedPaidToDate(start))
	equalAmount(t, "200000.00", d.ExpectedPaidToDate(weeksIn(10)))
	equalAmount(t, "1000000.00", d.ExpectedPaidToDate(weeksIn(50)))
}

func TestArrearsWhenBehindSchedule(t *testing.T) {
	// 10 weeks in, 200,000 expected, only 150,000 paid.
	d := newDeployment(150_000)
	equalAmount(t, "50000.00", d.Arrears(weeksIn(10)))
	equalAmount(t, "0.00", d.AheadBy(weeksIn(10)))
	assert.Equal(t, ScheduleBehind, d.ScheduleStatus(weeksIn(10)))
	assert.InDelta(t, -2.5, d.WeeksOfCoverage(weeksIn(10)), 0.001)
}

func TestAheadWhenPayingFaster(t *testing.T) {
	d := newDeployment(300_000)
	equalAmount(t, "0.00", d.Arrears(weeksIn(10)))
	equalAmount(t, "100000.00", d.AheadBy(weeksIn(10)))
	assert.Equal(t, ScheduleAhead, d.ScheduleStatus(weeksIn(10)))
	assert.InDelta(t, 5.0, d.WeeksOfCoverage(weeksIn(10)), 0.001)
}

// A customer one instalment short is chased by the collections engine; a
// customer a few days late is not. The tolerance is exactly one weekly due.
func TestScheduleStatusToleratesUpToOneWeeklyInstalment(t *testing.T) {
	onTrack := newDeployment(180_000) // exactly one instalment short
	assert.Equal(t, ScheduleOnTrack, onTrack.ScheduleStatus(weeksIn(10)))

	behind := newDeployment(179_999)
	assert.Equal(t, ScheduleBehind, behind.ScheduleStatus(weeksIn(10)))
}

func TestScheduleStatusIsSettledOnceRepaid(t *testing.T) {
	d := newDeployment(1_000_000)
	d.Status = StatusCompleted
	assert.Equal(t, ScheduleSettled, d.ScheduleStatus(weeksIn(20)))
	equalAmount(t, "0.00", d.Arrears(weeksIn(60)), "a settled asset never accrues arrears")
}

func TestPercentRepaidCapsAtHundred(t *testing.T) {
	assert.InDelta(t, 30.0, newDeployment(300_000).PercentRepaid(), 0.001)
	assert.InDelta(t, 100.0, newDeployment(1_500_000).PercentRepaid(), 0.001)
	assert.InDelta(t, 0.0, newDeployment(0).PercentRepaid(), 0.001)
}

func TestProjectedCompletionTracksRunRate(t *testing.T) {
	// Paying 10,000/week against a 20,000/week schedule: 900,000 left at
	// 10,000/week is 90 more weeks.
	d := newDeployment(100_000)
	projected := d.ProjectedCompletionDate(weeksIn(10))
	require.NotNil(t, projected)
	assert.Equal(t, weeksIn(100), *projected)
	assert.True(t, projected.After(d.ExpectedCompletionDate()), "under-payer must project past term")
}

func TestProjectedCompletionIsNilWithoutHistory(t *testing.T) {
	assert.Nil(t, newDeployment(0).ProjectedCompletionDate(weeksIn(5)), "no payments, no run rate")
	assert.Nil(t, newDeployment(20_000).ProjectedCompletionDate(start), "no elapsed weeks")
	assert.Nil(t, newDeployment(1_000_000).ProjectedCompletionDate(weeksIn(20)), "nothing left to project")
}

func TestExpectedCompletionDateIsStartPlusTerm(t *testing.T) {
	assert.Equal(t, weeksIn(50), newDeployment(0).ExpectedCompletionDate())
}

func TestSplitPaymentCapsAtOutstanding(t *testing.T) {
	principal := money.FromInt(1_000_000)

	applied, excess := SplitPayment(principal, money.FromInt(300_000), money.FromInt(10_000))
	equalAmount(t, "10000.00", applied)
	equalAmount(t, "0.00", excess)

	// 50,000 against a 30,000 balance: 30,000 clears it, 20,000 is credit.
	applied, excess = SplitPayment(principal, money.FromInt(970_000), money.FromInt(50_000))
	equalAmount(t, "30000.00", applied)
	equalAmount(t, "20000.00", excess)

	// Already settled: the whole payment is credit.
	applied, excess = SplitPayment(principal, money.FromInt(1_000_000), money.FromInt(5_000))
	equalAmount(t, "0.00", applied)
	equalAmount(t, "5000.00", excess)
}

func TestPositionAtReturnsEveryComputedField(t *testing.T) {
	d := newDeployment(150_000)
	d.PaymentCount = 15

	p := d.PositionAt(weeksIn(10))

	assert.Equal(t, "GIG00001", p.CustomerID)
	assert.Equal(t, "NGN", p.Currency.Code)
	equalAmount(t, "1000000.00", p.Principal)
	equalAmount(t, "150000.00", p.AmountPaid)
	equalAmount(t, "850000.00", p.Outstanding)
	equalAmount(t, "20000.00", p.WeeklyDue)
	equalAmount(t, "200000.00", p.ExpectedPaidToDate)
	equalAmount(t, "50000.00", p.Arrears)
	assert.Equal(t, ScheduleBehind, p.ScheduleStatus)
	assert.Equal(t, 10, p.WeeksElapsed)
	assert.Equal(t, 50, p.TermWeeks)
	assert.Equal(t, int64(15), p.PaymentCount)
	assert.Equal(t, weeksIn(50), p.ExpectedCompletionDate)
	assert.NotNil(t, p.ProjectedCompletionDate)
}

// The full 50-week happy path: exactly 50 instalments settle the asset with no
// drift and no residual minor units.
func TestFiftyWeeklyInstalmentsSettleExactly(t *testing.T) {
	d := newDeployment(0)
	for week := 1; week <= 50; week++ {
		d.AmountPaid = d.AmountPaid.Add(d.WeeklyDue())
	}
	assert.True(t, d.AmountPaid.Equal(d.Principal), "paid %s, principal %s", d.AmountPaid, d.Principal)
	equalAmount(t, "0.00", d.Outstanding())
	equalAmount(t, "0.00", d.Arrears(weeksIn(50)))
	assert.InDelta(t, 100.0, d.PercentRepaid(), 0.001)
}

// A term the principal does not divide evenly is where an integer-minor-unit
// design leaks value: the weekly instalment truncates, and the shortfall has
// to land somewhere. Pro-rata expectations keep it in the final instalment.
func TestUnevenTermLeavesTheRemainderInTheFinalInstalment(t *testing.T) {
	d := newDeployment(0)
	d.TermWeeks = 3

	equalAmount(t, "333333.33", d.WeeklyDue())
	equalAmount(t, "666666.67", d.ExpectedPaidToDate(weeksIn(2)))
	equalAmount(t, "1000000.00", d.ExpectedPaidToDate(weeksIn(3)),
		"the full principal is due by the end of the term, remainder included")
}
