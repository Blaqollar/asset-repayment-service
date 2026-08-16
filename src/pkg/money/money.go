package money

import (
	"errors"
	"strings"

	"github.com/shopspring/decimal"
)

// Amount is an exact decimal.
type Amount = decimal.Decimal

// Zero is the additive identity, exported so callers need not import decimal.
var Zero = decimal.Zero

// maxIntegerDigits keeps a parsed amount inside the NUMERIC(24,6) columns.
const maxIntegerDigits = 18

var (
	ErrInvalidAmount = errors.New("must be a valid decimal amount")
	ErrNegativeAmount = errors.New("must be greater than zero")
	ErrAmountTooLarge = errors.New("exceeds the maximum representable amount")
	ErrTooPrecise = errors.New("has more decimal places than the currency allows")
)

// Currency describes how amounts in one currency are read and written.
type Currency struct {
	Code   string // ISO 4217 code, or any agreed ticker
	Symbol string
	Scale  int32 // decimal places the currency admits
}

// defaultScale applies to unknown currencies; 2dp is right for most.
const defaultScale = 2

//This should be moved to a config file or database in the future, but for now it's hardcoded for simplicity.
var currencies = map[string]Currency{
	"NGN": {Code: "NGN", Symbol: "₦", Scale: 2},
	"USD": {Code: "USD", Symbol: "$", Scale: 2},
	"EUR": {Code: "EUR", Symbol: "€", Scale: 2},
	"GBP": {Code: "GBP", Symbol: "£", Scale: 2},
	"KES": {Code: "KES", Symbol: "KSh", Scale: 2},
	"GHS": {Code: "GHS", Symbol: "₵", Scale: 2},
	"ZAR": {Code: "ZAR", Symbol: "R", Scale: 2},
	"JPY": {Code: "JPY", Symbol: "¥", Scale: 0},
	"KWD": {Code: "KWD", Symbol: "KD", Scale: 3},
	"TND": {Code: "TND", Symbol: "DT", Scale: 3},
}

// Lookup returns the currency for a code. 
func Lookup(code string) Currency {
	normalised := strings.ToUpper(strings.TrimSpace(code))
	if found, ok := currencies[normalised]; ok {
		return found
	}
	return Currency{Code: normalised, Scale: defaultScale}
}

func (c Currency) String() string { return c.Code }

// Parse converts a provider string ("10000", "10,000.50", "₦10,000.50") into
// an exact amount. The bank sends amounts as strings, so this is the hot path.
func Parse(raw string, currency Currency) (Amount, error) {
	cleaned := strings.TrimSpace(raw)
	cleaned = strings.ReplaceAll(cleaned, ",", "")
	cleaned = strings.ReplaceAll(cleaned, " ", "")
	cleaned = strings.TrimPrefix(cleaned, currency.Code)
	if currency.Symbol != "" {
		cleaned = strings.TrimPrefix(cleaned, currency.Symbol)
	}
	if cleaned == "" {
		return Zero, ErrInvalidAmount
	}

	amount, err := decimal.NewFromString(cleaned)
	if err != nil {
		return Zero, ErrInvalidAmount
	}

	if len(amount.Truncate(0).Abs().String()) > maxIntegerDigits {
		return Zero, ErrAmountTooLarge
	}
	// Rejected, not rounded: discarding minor units is a reconciliation
	// defect, and rounding up creates money.
	if -amount.Exponent() > currency.Scale {
		return Zero, ErrTooPrecise
	}
	return amount, nil
}

// ParsePositive rejects zero and negative amounts: a credit moves money one
// way, and a reversal is a separate event, not a negative credit.
func ParsePositive(raw string, currency Currency) (Amount, error) {
	amount, err := Parse(raw, currency)
	if err != nil {
		return Zero, err
	}
	if !amount.IsPositive() {
		return Zero, ErrNegativeAmount
	}
	return amount, nil
}

// Format renders at the currency's precision ("10000.00" NGN, "10000" JPY), so
// no consumer guesses a scale or loses precision to a JSON float.
func Format(amount Amount, currency Currency) string {
	return amount.StringFixed(currency.Scale)
}

// FromInt builds an amount from whole currency units.
func FromInt(units int64) Amount { return decimal.NewFromInt(units) }

// Min returns the smaller of two amounts.
func Min(a, b Amount) Amount { return decimal.Min(a, b) }

// Max returns the larger of two amounts.
func Max(a, b Amount) Amount { return decimal.Max(a, b) }

// NonNegative floors an amount at zero.
func NonNegative(amount Amount) Amount { return Max(amount, Zero) }

// Prorate computes amount × num ÷ den at the currency's precision — dividing
// first would throw away the remainder.
func Prorate(amount Amount, numerator, denominator int64, currency Currency) Amount {
	if denominator == 0 {
		return Zero
	}
	return amount.Mul(decimal.NewFromInt(numerator)).
		DivRound(decimal.NewFromInt(denominator), currency.Scale)
}
