package payment

import (
	"encoding/json"
	"testing"
	"time"

	"asset-repayment-service/pkg/money"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

var (
	lagos = time.FixedZone("WAT", 1*60*60)
	ngn   = money.Lookup("NGN")
)

func sampleNotification() Notification {
	return Notification{
		CustomerID:           "GIG00001",
		PaymentStatus:        "COMPLETE",
		TransactionAmount:    "10000",
		TransactionDate:      "2025-11-07 14:54:16",
		TransactionReference: "VPAY25110713542114478761522000",
	}
}

func TestValidateAcceptsTheDocumentedPayload(t *testing.T) {
	validated, err := sampleNotification().Validate(lagos, ngn, json.RawMessage(`{}`))
	require.NoError(t, err)

	assert.Equal(t, "GIG00001", validated.CustomerID)
	assert.Equal(t, "VPAY25110713542114478761522000", validated.Reference)
	assert.Equal(t, "10000.00", money.Format(validated.Amount, ngn))
	assert.Equal(t, "NGN", validated.Currency.Code)
	assert.Equal(t, "COMPLETE", validated.ProviderStatus)

	// 14:54:16 WAT is 13:54:16 UTC. Storing the naive timestamp as UTC would
	// place the payment an hour early and can shift it across a week boundary.
	assert.Equal(t, time.Date(2025, 11, 7, 13, 54, 16, 0, time.UTC), validated.TransactionDate)
}

func TestValidateReportsEveryBadFieldAtOnce(t *testing.T) {
	_, err := Notification{
		CustomerID:           "",
		PaymentStatus:        "",
		TransactionAmount:    "not-a-number",
		TransactionDate:      "yesterday",
		TransactionReference: "x",
	}.Validate(lagos, ngn, nil)

	require.Error(t, err)

	// One round trip should tell an integrating provider everything that is
	// wrong with their payload, not just the first problem encountered.
	fields := errDetails(t, err)
	assert.Contains(t, fields, "customer_id")
	assert.Contains(t, fields, "payment_status")
	assert.Contains(t, fields, "transaction_amount")
	assert.Contains(t, fields, "transaction_date")
	assert.Contains(t, fields, "transaction_reference")
}

func TestValidateRejectsNonPositiveAmounts(t *testing.T) {
	for _, amount := range []string{"0", "-5000", "0.00"} {
		t.Run(amount, func(t *testing.T) {
			n := sampleNotification()
			n.TransactionAmount = amount

			_, err := n.Validate(lagos, ngn, nil)
			require.Error(t, err)
			assert.Contains(t, errDetails(t, err), "transaction_amount")
		})
	}
}

func TestValidateNormalisesWhitespaceAndCase(t *testing.T) {
	n := sampleNotification()
	n.CustomerID = "  GIG00001  "
	n.PaymentStatus = " complete "
	n.TransactionReference = "  VPAY25110713542114478761522000  "

	validated, err := n.Validate(lagos, ngn, nil)
	require.NoError(t, err)
	assert.Equal(t, "GIG00001", validated.CustomerID)
	assert.Equal(t, "COMPLETE", validated.ProviderStatus)
	assert.Equal(t, "VPAY25110713542114478761522000", validated.Reference)
}

func TestIsSuccessfulOnlyAcceptsSettledFunds(t *testing.T) {
	for _, status := range []string{"COMPLETE", "completed", " Success ", "SUCCESSFUL"} {
		assert.True(t, IsSuccessful(status), "%q should count as settled", status)
	}
	// A pending credit that is later reversed would hand the customer an asset
	// they have not paid for, so nothing but a settled status may apply.
	for _, status := range []string{"PENDING", "FAILED", "REVERSED", "PROCESSING", "", "COMPLET"} {
		assert.False(t, IsSuccessful(status), "%q must not count as settled", status)
	}
}

func TestParseTransactionDateAcceptsProviderVariants(t *testing.T) {
	expected := time.Date(2025, 11, 7, 13, 54, 16, 0, time.UTC)

	for _, layout := range []string{
		"2025-11-07 14:54:16",
		"2025-11-07T14:54:16",
	} {
		t.Run(layout, func(t *testing.T) {
			parsed, err := ParseTransactionDate(layout, lagos)
			require.NoError(t, err)
			assert.Equal(t, expected, parsed)
		})
	}

	// An explicit offset must win over the assumed provider zone.
	parsed, err := ParseTransactionDate("2025-11-07T14:54:16Z", lagos)
	require.NoError(t, err)
	assert.Equal(t, time.Date(2025, 11, 7, 14, 54, 16, 0, time.UTC), parsed)
}

func TestParseTransactionDateRejectsGarbage(t *testing.T) {
	_, err := ParseTransactionDate("", lagos)
	assert.ErrorIs(t, err, ErrMissingTransactionDate)

	_, err = ParseTransactionDate("07-11-2025 lunchtime", lagos)
	assert.ErrorIs(t, err, ErrInvalidTransactionDate)
}

func TestValidateRetainsRawPayload(t *testing.T) {
	raw := json.RawMessage(`{"customer_id":"GIG00001","extra":"provider-specific"}`)

	validated, err := sampleNotification().Validate(lagos, ngn, raw)
	require.NoError(t, err)
	assert.JSONEq(t, string(raw), string(validated.RawPayload),
		"the original payload must survive for dispute resolution")
}
