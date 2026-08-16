package payment

import (
	"encoding/json"
	"strings"
	"time"

	"asset-repayment-service/pkg/money"
	"github.com/google/uuid"
)

// Notification is the raw inbound payload from the bank
type Notification struct {
	CustomerID           string `json:"customer_id"`
	PaymentStatus        string `json:"payment_status"`
	TransactionAmount    string `json:"transaction_amount"`
	TransactionDate      string `json:"transaction_date"`
	TransactionReference string `json:"transaction_reference"`
	Currency string `json:"currency,omitempty"`
}

// ValidatedNotification is a parsed Notification. Only this type is persisted.
type ValidatedNotification struct {
	CustomerID      string
	Reference       string
	Currency        money.Currency
	Amount          money.Amount
	ProviderStatus  string
	TransactionDate time.Time
	RawPayload      json.RawMessage
	ReceivedAt      time.Time
}

// Payment is one credit recorded against a customer, applied or not.
type Payment struct {
	UUID                 uuid.UUID
	TransactionReference string
	CustomerID           string
	DeploymentID         *int64
	DeploymentUUID       *uuid.UUID

	Currency money.Currency
	Amount   money.Amount
	Applied  money.Amount
	Excess   money.Amount

	// Snapshots taken inside the write, so any row is auditable on its own.
	BalanceBefore money.Amount
	BalanceAfter  money.Amount

	Outcome         string
	ProviderStatus  string
	TransactionDate time.Time
	ReceivedAt      time.Time
	CreatedAt       time.Time
	RawPayload      json.RawMessage
}

// Outcomes of processing a notification.
const (
	// OutcomeApplied means the credit reduced the customer's outstanding balance.
	OutcomeApplied = "applied"
	// OutcomeDuplicate means this transaction_reference was already processed;
	// the original result is replayed and no money moves a second time.
	OutcomeDuplicate = "duplicate"
	// OutcomeUnmatched means no open deployment matched; recorded for
	// reconciliation, because money that arrived is never discarded.
	OutcomeUnmatched = "unmatched"
	// OutcomeIgnored means the provider reported a non-successful status, so
	// the notification is recorded for audit but not applied.
	OutcomeIgnored = "ignored"
	// OutcomeQueued means the credit was accepted and durably queued. It is
	// never stored: by the time a row exists, the money has moved or not.
	OutcomeQueued = "queued"
)

// Provider payment statuses that represent settled, spendable funds.
var successStatuses = map[string]struct{}{
	"COMPLETE":   {},
	"COMPLETED":  {},
	"SUCCESS":    {},
	"SUCCESSFUL": {},
}

// IsSuccessful reports whether the funds have settled.
func IsSuccessful(providerStatus string) bool {
	_, ok := successStatuses[strings.ToUpper(strings.TrimSpace(providerStatus))]
	return ok
}

// Layouts accepted for transaction_date, most specific first. 
var dateLayouts = []string{
	"2006-01-02 15:04:05",
	"2006-01-02T15:04:05",
	"2006-01-02 15:04:05.000",
	time.RFC3339,
	time.RFC3339Nano,
	"2006-01-02",
	"02/01/2006 15:04:05",
}

// ParseTransactionDate interprets the provider timestamp.
func ParseTransactionDate(value string, loc *time.Location) (time.Time, error) {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" {
		return time.Time{}, ErrMissingTransactionDate
	}
	if loc == nil {
		loc = time.UTC
	}

	for _, layout := range dateLayouts {
		// ParseInLocation, so naive layouts adopt the provider's zone.
		if parsed, err := time.ParseInLocation(layout, trimmed, loc); err == nil {
			return parsed.UTC(), nil
		}
	}
	return time.Time{}, ErrInvalidTransactionDate
}

const (
	minReferenceLength = 6
	maxReferenceLength = 128
	maxCustomerIDLen   = 64
)

// Validate parses and checks the notification.
func (n Notification) Validate(loc *time.Location, defaultCurrency money.Currency, raw json.RawMessage) (*ValidatedNotification, error) {
	details := map[string]any{}

	customerID := strings.TrimSpace(n.CustomerID)
	switch {
	case customerID == "":
		details["customer_id"] = "is required"
	case len(customerID) > maxCustomerIDLen:
		details["customer_id"] = "exceeds maximum length"
	}

	reference := strings.TrimSpace(n.TransactionReference)
	switch {
	case reference == "":
		details["transaction_reference"] = "is required"
	case len(reference) < minReferenceLength:
		details["transaction_reference"] = "is too short to be a valid provider reference"
	case len(reference) > maxReferenceLength:
		details["transaction_reference"] = "exceeds maximum length"
	}

	currency := defaultCurrency
	if strings.TrimSpace(n.Currency) != "" {
		currency = money.Lookup(n.Currency)
	}

	amount, err := money.ParsePositive(n.TransactionAmount, currency)
	if err != nil {
		details["transaction_amount"] = err.Error()
	}

	status := strings.ToUpper(strings.TrimSpace(n.PaymentStatus))
	if status == "" {
		details["payment_status"] = "is required"
	}

	txnDate, err := ParseTransactionDate(n.TransactionDate, loc)
	if err != nil {
		details["transaction_date"] = err.Error()
	}

	if len(details) > 0 {
		return nil, ErrInvalidNotification.WithDetails(details)
	}

	return &ValidatedNotification{
		CustomerID:      customerID,
		Reference:       reference,
		Currency:        currency,
		Amount:          amount,
		ProviderStatus:  status,
		TransactionDate: txnDate,
		RawPayload:      raw,
		ReceivedAt:      time.Now().UTC(),
	}, nil
}
