package money

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

var (
	ngn = Lookup("NGN")
	jpy = Lookup("JPY")
	kwd = Lookup("KWD")
)

func TestParseAcceptsValidAmounts(t *testing.T) {
	cases := map[string]string{
		"10000":      "10000.00",
		"10000.00":   "10000.00",
		"10000.5":    "10000.50",
		"0.01":       "0.01",
		".5":         "0.50",
		"1,000,000":  "1000000.00",
		"  10000  ":  "10000.00",
		"₦10000.99":  "10000.99",
		"NGN 250.25": "250.25",
	}

	for input, expected := range cases {
		t.Run(input, func(t *testing.T) {
			got, err := Parse(input, ngn)
			require.NoError(t, err)
			assert.Equal(t, expected, Format(got, ngn))
		})
	}
}

// A float64 round-trip of these values loses a minor unit; a decimal must not.
func TestParseIsExactForFloatHostileValues(t *testing.T) {
	for _, input := range []string{"10000.10", "0.29", "1.15", "8.20", "70.70"} {
		t.Run(input, func(t *testing.T) {
			got, err := Parse(input, ngn)
			require.NoError(t, err)
			assert.Equal(t, input, Format(got, ngn), "precision lost on %s", input)
		})
	}
}

func TestParseRejectsMalformedAmounts(t *testing.T) {
	for _, input := range []string{"", "   ", "abc", "10.00.00", "--5"} {
		t.Run(input, func(t *testing.T) {
			_, err := Parse(input, ngn)
			assert.ErrorIs(t, err, ErrInvalidAmount)
		})
	}
}

func TestParseRejectsAmountsBeyondTheCurrencyPrecision(t *testing.T) {
	_, err := Parse("100.999", ngn)
	assert.ErrorIs(t, err, ErrTooPrecise)

	// The same figure is legal in a 3dp currency, and illegal in a 0dp one.
	_, err = Parse("100.999", kwd)
	assert.NoError(t, err)

	_, err = Parse("100.5", jpy)
	assert.ErrorIs(t, err, ErrTooPrecise)
}

func TestParseRejectsAmountsTooLargeForTheColumn(t *testing.T) {
	_, err := Parse("1234567890123456789", ngn)
	assert.ErrorIs(t, err, ErrAmountTooLarge)
}

func TestParsePositiveRejectsZeroAndNegative(t *testing.T) {
	_, err := ParsePositive("0", ngn)
	assert.ErrorIs(t, err, ErrNegativeAmount)

	_, err = ParsePositive("-100", ngn)
	assert.ErrorIs(t, err, ErrNegativeAmount)
}

func TestFormatUsesTheCurrencyPrecision(t *testing.T) {
	amount, err := Parse("10000.5", ngn)
	require.NoError(t, err)

	assert.Equal(t, "10000.50", Format(amount, ngn))
	assert.Equal(t, "10000.500", Format(amount, kwd))
	assert.Equal(t, "10001", Format(amount, jpy))
}

func TestProrateKeepsRemainderPrecision(t *testing.T) {
	// 1,000,000 over 50 weeks, 3 weeks elapsed => 60,000 expected.
	principal := FromInt(1_000_000)
	assert.Equal(t, "60000.00", Format(Prorate(principal, 3, 50, ngn), ngn))

	// A term that does not divide evenly must not lose minor units to an
	// early division.
	assert.Equal(t, "333333.33", Format(Prorate(principal, 1, 3, ngn), ngn))

	// Division by zero is a caller bug, not a panic.
	assert.True(t, Prorate(principal, 1, 0, ngn).IsZero())
}

func TestNonNegativeFloorsAtZero(t *testing.T) {
	assert.Equal(t, "0.00", Format(NonNegative(FromInt(-5)), ngn))
	assert.Equal(t, "5.00", Format(NonNegative(FromInt(5)), ngn))
}

func TestLookupFallsBackToTwoDecimalPlaces(t *testing.T) {
	unknown := Lookup("xyz")
	assert.Equal(t, "XYZ", unknown.Code)
	assert.Equal(t, int32(2), unknown.Scale)
}
