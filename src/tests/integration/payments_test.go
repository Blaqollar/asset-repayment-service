//go:build integration

package integration

import (
	"fmt"
	"net/http"
	"sync"
	"testing"

	"asset-repayment-service/internal/application/dtos"
	"github.com/shopspring/decimal"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestApplyPaymentReducesOutstandingBalance(t *testing.T) {
	reset(t)
	seedDeployment(t, "GIG00001", 10)

	status, resp := post(t, "/api/v1/payments",
		notification("GIG00001", "VPAY25110713542114478761522000", "10000"))
	require.Equal(t, http.StatusOK, status, resp.Message)

	var applied dtos.ApplyPaymentResponse
	unmarshalData(t, resp, &applied)

	assert.Equal(t, "applied", applied.Outcome)
	assert.False(t, applied.Duplicate)
	assert.Equal(t, "10000.00", applied.Payment.AmountApplied)
	assert.Equal(t, "0.00", applied.Payment.Excess)
	assert.Equal(t, "0.00", applied.Payment.BalanceBefore)
	assert.Equal(t, "10000.00", applied.Payment.BalanceAfter)

	require.NotNil(t, applied.Position)
	assert.Equal(t, "1000000.00", applied.Position.Principal)
	assert.Equal(t, "10000.00", applied.Position.TotalPaid)
	assert.Equal(t, "990000.00", applied.Position.Outstanding)
	assert.Equal(t, "20000.00", applied.Position.WeeklyDue)
	assert.Equal(t, int64(1), applied.Position.PaymentCount)

	// Ten weeks in, 200,000 should have been repaid; 10,000 has been.
	assert.Equal(t, "200000.00", applied.Position.ExpectedPaidToDate)
	assert.Equal(t, "190000.00", applied.Position.Arrears)
	assert.Equal(t, "behind", applied.Position.ScheduleStatus)

	requireAmount(t, "10000", amountPaid(t, "GIG00001"))
}

// The single most important property: a provider that delivers the same
// notification twice must not clear the customer's debt twice.
func TestDuplicateReferenceIsAppliedExactlyOnce(t *testing.T) {
	reset(t)
	seedDeployment(t, "GIG00002", 10)

	payload := notification("GIG00002", "VPAY-DUPLICATE-0001", "25000")

	status, first := post(t, "/api/v1/payments", payload)
	require.Equal(t, http.StatusOK, status)

	var firstResult dtos.ApplyPaymentResponse
	unmarshalData(t, first, &firstResult)
	assert.Equal(t, "applied", firstResult.Outcome)

	// Replay it three more times, exactly as a retrying provider would.
	for attempt := 0; attempt < 3; attempt++ {
		status, replay := post(t, "/api/v1/payments", payload)
		require.Equal(t, http.StatusOK, status, "a replay must not be an error")

		var replayResult dtos.ApplyPaymentResponse
		unmarshalData(t, replay, &replayResult)

		assert.Equal(t, "duplicate", replayResult.Outcome)
		assert.True(t, replayResult.Duplicate)
		// The replay reports the original amounts, not a fresh application.
		assert.Equal(t, "25000.00", replayResult.Payment.AmountApplied)
		assert.Equal(t, "25000.00", replayResult.Payment.BalanceAfter)
	}

	requireAmount(t, "25000", amountPaid(t, "GIG00002"), "balance moved more than once")

	var rows int
	require.NoError(t, sharedDB.Get(&rows,
		`SELECT count(*) FROM payments WHERE transaction_reference = $1`, "VPAY-DUPLICATE-0001"))
	assert.Equal(t, 1, rows, "the ledger must hold exactly one entry per reference")
}

// The same notification arriving on many connections at the same instant is
// the case a naive check-then-write gets wrong. Only the unique constraint
// makes this safe, and this test is what proves it.
func TestConcurrentDuplicatesApplyExactlyOnce(t *testing.T) {
	reset(t)
	seedDeployment(t, "GIG00003", 5)

	const concurrency = 40
	payload := notification("GIG00003", "VPAY-RACE-0001", "15000")

	outcomes := make([]string, concurrency)
	failures := make([]error, concurrency)
	var wg sync.WaitGroup
	start := make(chan struct{})

	for i := 0; i < concurrency; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			<-start // release them together to maximise overlap

			_, resp, err := postAsync("/api/v1/payments", payload)
			if err != nil {
				failures[idx] = err
				return
			}

			var result dtos.ApplyPaymentResponse
			if err := unmarshalInto(resp, &result); err != nil {
				failures[idx] = err
				return
			}
			outcomes[idx] = result.Outcome
		}(i)
	}

	close(start)
	wg.Wait()

	for _, err := range failures {
		require.NoError(t, err)
	}

	appliedCount := 0
	for _, outcome := range outcomes {
		if outcome == "applied" {
			appliedCount++
		}
	}

	assert.Equal(t, 1, appliedCount, "exactly one racer may apply the payment")
	requireAmount(t, "15000", amountPaid(t, "GIG00003"))
	assert.True(t, ledgerTotal(t, "GIG00003").Equal(amountPaid(t, "GIG00003")))
}

// Distinct payments landing concurrently on one deployment must all be
// counted. This is the lost-update case: a read-modify-write in application
// code would silently drop some of these.
func TestConcurrentDistinctPaymentsAllApply(t *testing.T) {
	reset(t)
	seedDeployment(t, "GIG00004", 20)

	const payments = 50
	const amount = 5_000

	failures := make([]error, payments)
	var wg sync.WaitGroup
	start := make(chan struct{})

	for i := 0; i < payments; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			<-start

			_, _, err := postAsync("/api/v1/payments",
				notification("GIG00004", fmt.Sprintf("VPAY-CONCURRENT-%04d", idx), "5000"))
			failures[idx] = err
		}(i)
	}

	close(start)
	wg.Wait()

	for _, err := range failures {
		require.NoError(t, err)
	}

	requireAmount(t, fmt.Sprint(payments*amount), amountPaid(t, "GIG00004"), "payments were lost to a race")
	assert.True(t, ledgerTotal(t, "GIG00004").Equal(amountPaid(t, "GIG00004")),
		"ledger and materialised balance disagree")

	var rows int
	require.NoError(t, sharedDB.Get(&rows,
		`SELECT count(*) FROM payments WHERE customer_id = $1 AND outcome = 'applied'`, "GIG00004"))
	assert.Equal(t, payments, rows)
}

func TestOverpaymentSettlesAssetAndCarriesCredit(t *testing.T) {
	reset(t)
	seedDeployment(t, "GIG00005", 49)

	// Clear almost all of it, then overshoot the remainder.
	status, _ := post(t, "/api/v1/payments", notification("GIG00005", "VPAY-BULK-0001", "980000"))
	require.Equal(t, http.StatusOK, status)

	status, resp := post(t, "/api/v1/payments", notification("GIG00005", "VPAY-BULK-0002", "50000"))
	require.Equal(t, http.StatusOK, status)

	var result dtos.ApplyPaymentResponse
	unmarshalData(t, resp, &result)

	// 50,000 against a 20,000 balance: 20,000 settles it, 30,000 is credit.
	assert.Equal(t, "20000.00", result.Payment.AmountApplied)
	assert.Equal(t, "30000.00", result.Payment.Excess)

	require.NotNil(t, result.Position)
	assert.Equal(t, "0.00", result.Position.Outstanding)
	assert.Equal(t, "30000.00", result.Position.Excess)
	assert.Equal(t, "completed", result.Position.Status, "a fully repaid asset transfers ownership")
	assert.Equal(t, "settled", result.Position.ScheduleStatus)
	assert.InDelta(t, 100.0, result.Position.PercentRepaid, 0.001)

	// The SQL split must agree with deployment.SplitPayment.
	var applied, excess decimal.Decimal
	require.NoError(t, sharedDB.QueryRow(
		`SELECT applied_amount, excess_amount FROM payments WHERE transaction_reference = $1`,
		"VPAY-BULK-0002").Scan(&applied, &excess))
	requireAmount(t, "20000", applied)
	requireAmount(t, "30000", excess)
}

// Fifty weekly instalments must settle a 1,000,000 / 50-week asset exactly,
// with no residual minor units and no drift.
func TestFiftyInstalmentsSettleExactly(t *testing.T) {
	reset(t)
	seedDeployment(t, "GIG00006", 50)

	for week := 1; week <= 50; week++ {
		status, _ := post(t, "/api/v1/payments",
			notification("GIG00006", fmt.Sprintf("VPAY-WEEK-%02d", week), "20000"))
		require.Equal(t, http.StatusOK, status)
	}

	status, resp := get(t, "/api/v1/customers/GIG00006/position")
	require.Equal(t, http.StatusOK, status)

	var position dtos.PositionDTO
	unmarshalData(t, resp, &position)

	assert.Equal(t, "1000000.00", position.TotalPaid)
	assert.Equal(t, "0.00", position.Outstanding)
	assert.Equal(t, "0.00", position.Excess)
	assert.Equal(t, "0.00", position.Arrears)
	assert.Equal(t, "completed", position.Status)
	assert.Equal(t, "settled", position.ScheduleStatus)
	assert.Equal(t, int64(50), position.PaymentCount)

	// A customer who has finished paying can still read where they stand —
	// the position above is served for a settled deployment, not refused.
	status, resp = get(t, "/api/v1/customers/GIG00006/position")
	require.Equal(t, http.StatusOK, status, resp.Message)
}

// Money that arrives for an unknown customer must never be discarded: it is
// recorded for reconciliation and the provider is told not to retry.
func TestUnknownCustomerPaymentIsRecordedNotDropped(t *testing.T) {
	reset(t)

	status, resp := post(t, "/api/v1/payments",
		notification("GIG-UNKNOWN", "VPAY-ORPHAN-0001", "10000"))

	require.Equal(t, http.StatusAccepted, status)

	var result dtos.ApplyPaymentResponse
	unmarshalData(t, resp, &result)
	assert.Equal(t, "unmatched", result.Outcome)
	assert.Nil(t, result.Position)

	var stored struct {
		Outcome      string          `db:"outcome"`
		Amount       decimal.Decimal `db:"amount"`
		DeploymentID *int64          `db:"deployment_id"`
	}
	require.NoError(t, sharedDB.Get(&stored,
		`SELECT outcome, amount, deployment_id FROM payments WHERE transaction_reference = $1`,
		"VPAY-ORPHAN-0001"))

	assert.Equal(t, "unmatched", stored.Outcome)
	requireAmount(t, "10000", stored.Amount)
	assert.Nil(t, stored.DeploymentID)
}

func TestUnsettledStatusIsRecordedButNotApplied(t *testing.T) {
	reset(t)
	seedDeployment(t, "GIG00007", 3)

	for _, providerStatus := range []string{"PENDING", "FAILED", "REVERSED"} {
		t.Run(providerStatus, func(t *testing.T) {
			payload := notification("GIG00007", "VPAY-UNSETTLED-"+providerStatus, "10000")
			payload["payment_status"] = providerStatus

			status, resp := post(t, "/api/v1/payments", payload)
			require.Equal(t, http.StatusAccepted, status)

			var result dtos.ApplyPaymentResponse
			unmarshalData(t, resp, &result)
			assert.Equal(t, "ignored", result.Outcome)
		})
	}

	requireAmount(t, "0", amountPaid(t, "GIG00007"), "unsettled funds must not move a balance")

	// They are still filed against the right deployment for reconciliation.
	var withDeployment int
	require.NoError(t, sharedDB.Get(&withDeployment,
		`SELECT count(*) FROM payments WHERE customer_id = $1 AND outcome = 'ignored' AND deployment_id IS NOT NULL`,
		"GIG00007"))
	assert.Equal(t, 3, withDeployment)
}

func TestMalformedPayloadIsRejectedWithFieldDetail(t *testing.T) {
	reset(t)

	status, resp := post(t, "/api/v1/payments", map[string]any{
		"customer_id":           "",
		"payment_status":        "COMPLETE",
		"transaction_amount":    "ten thousand",
		"transaction_date":      "not-a-date",
		"transaction_reference": "VPAY-BAD-0001",
	})

	require.Equal(t, http.StatusBadRequest, status)
	assert.Equal(t, "error", resp.Status)
	assert.Contains(t, resp.Errors, "customer_id")
	assert.Contains(t, resp.Errors, "transaction_amount")
	assert.Contains(t, resp.Errors, "transaction_date")

	// Nothing may be persisted from a payload that could not be understood.
	var rows int
	require.NoError(t, sharedDB.Get(&rows, `SELECT count(*) FROM payments`))
	assert.Zero(t, rows)
}

// The service exposes no statement endpoint, but the ledger it writes is still
// the record of what happened — so this asserts on the rows themselves.
func TestEveryAppliedPaymentIsRecordedInTheLedger(t *testing.T) {
	reset(t)
	seedDeployment(t, "GIG00008", 4)

	for i := 1; i <= 3; i++ {
		status, _ := post(t, "/api/v1/payments",
			notification("GIG00008", fmt.Sprintf("VPAY-HIST-%03d", i), "20000"))
		require.Equal(t, http.StatusOK, status)
	}

	var rows int
	require.NoError(t, sharedDB.Get(&rows,
		`SELECT count(*) FROM payments WHERE customer_id = $1 AND outcome = 'applied'`, "GIG00008"))
	assert.Equal(t, 3, rows)

	requireAmount(t, "60000", amountPaid(t, "GIG00008"))
	assert.True(t, ledgerTotal(t, "GIG00008").Equal(amountPaid(t, "GIG00008")))
}

// A reference identifies one request, not one customer and not one amount.
// Reusing it with different details would replay figures for a request that
// never happened, so it is a conflict rather than a duplicate.
func TestReferenceReusedWithDifferentDetailsIsRejected(t *testing.T) {
	reset(t)
	seedDeployment(t, "GIG00010", 4)
	seedDeployment(t, "GIG00011", 4)

	status, resp := post(t, "/api/v1/payments",
		notification("GIG00010", "VPAY-SHARED-REF-01", "10000"))
	require.Equal(t, http.StatusOK, status, resp.Message)

	// Same reference, different customer.
	status, resp = post(t, "/api/v1/payments",
		notification("GIG00011", "VPAY-SHARED-REF-01", "10000"))
	assert.Equal(t, http.StatusConflict, status, resp.Message)
	assert.Contains(t, resp.Errors, "customer_id")

	// Same reference and customer, different amount.
	status, resp = post(t, "/api/v1/payments",
		notification("GIG00010", "VPAY-SHARED-REF-01", "99000"))
	assert.Equal(t, http.StatusConflict, status, resp.Message)
	assert.Contains(t, resp.Errors, "transaction_amount")

	// An identical retry is still an ordinary replay.
	status, resp = post(t, "/api/v1/payments",
		notification("GIG00010", "VPAY-SHARED-REF-01", "10000"))
	require.Equal(t, http.StatusOK, status, resp.Message)

	var replay dtos.ApplyPaymentResponse
	unmarshalData(t, resp, &replay)
	assert.Equal(t, "duplicate", replay.Outcome)

	// No balance moved on any of the follow-ups.
	requireAmount(t, "10000", amountPaid(t, "GIG00010"))
	requireAmount(t, "0", amountPaid(t, "GIG00011"))
}

func TestPositionForUnknownCustomerIsNotFound(t *testing.T) {
	reset(t)

	status, resp := get(t, "/api/v1/customers/GIG-NOBODY/position")
	assert.Equal(t, http.StatusNotFound, status)
	assert.Equal(t, "error", resp.Status)
}

func TestOneOpenDeploymentPerCustomerIsEnforced(t *testing.T) {
	reset(t)
	seedDeployment(t, "GIG00009", 1)

	status, resp := post(t, "/api/v1/deployments", map[string]any{
		"customer_id": "GIG00009",
		"asset_id":    "AST-SECOND",
	})

	assert.Equal(t, http.StatusConflict, status, resp.Message)
}

func TestReadinessReportsDependencies(t *testing.T) {
	status, resp := get(t, "/readyz")
	require.Equal(t, http.StatusOK, status)

	var body map[string]string
	unmarshalData(t, resp, &body)
	assert.Equal(t, "ok", body["database"])
}

// Nothing in the schema, the domain or the API assumes a two-decimal currency.
// A yen deployment is repaid in whole yen, and a fractional yen is not money.
func TestCurrencyWithADifferentPrecisionWorksEndToEnd(t *testing.T) {
	reset(t)

	status, resp := post(t, "/api/v1/deployments", map[string]any{
		"customer_id": "JPY00001",
		"asset_id":    "AST-JPY",
		"currency":    "JPY",
		"principal":   "5000000",
		"term_weeks":  50,
	})
	require.Equal(t, http.StatusCreated, status, resp.Message)

	payload := notification("JPY00001", "VPAY-JPY-0000000001", "100000")
	payload["currency"] = "JPY"

	status, resp = post(t, "/api/v1/payments", payload)
	require.Equal(t, http.StatusOK, status, resp.Message)

	var result dtos.ApplyPaymentResponse
	unmarshalData(t, resp, &result)

	assert.Equal(t, "JPY", result.Payment.Currency)
	assert.Equal(t, "100000", result.Payment.AmountApplied, "yen carries no decimal places")

	require.NotNil(t, result.Position)
	assert.Equal(t, "4900000", result.Position.Outstanding)
	assert.Equal(t, "100000", result.Position.WeeklyDue)

	// Half a yen does not exist, so the notification is rejected rather than
	// silently rounded into or out of existence.
	fractional := notification("JPY00001", "VPAY-JPY-0000000002", "100.5")
	fractional["currency"] = "JPY"

	status, resp = post(t, "/api/v1/payments", fractional)
	require.Equal(t, http.StatusBadRequest, status)
	assert.Contains(t, resp.Errors, "transaction_amount")
}
