package payment

import (
	"errors"

	pkgerrors "asset-repayment-service/pkg/errors"
)

var (
	// ErrInvalidNotification carries per-field detail for a malformed payload.
	ErrInvalidNotification = pkgerrors.BadRequest("invalid payment notification")

	// ErrReferenceConflict means the reference was already used for a different request.
	ErrReferenceConflict = pkgerrors.Conflict("transaction_reference has already been used with different details")

	// ErrDeploymentClosed guards against applying to a settled deployment.
	ErrDeploymentClosed = pkgerrors.FailedPrecondition("deployment is no longer open for repayment")

	// ErrOverloaded is returned when the service sheds load instead of queueing
	// work it cannot finish inside the provider's timeout.
	ErrOverloaded = pkgerrors.TooManyRequests("payment ingestion is at capacity, retry shortly")
)

// Sentinel parse errors, wrapped by ErrInvalidNotification with field detail.
var (
	ErrMissingTransactionDate = errors.New("is required")
	ErrInvalidTransactionDate = errors.New("must be a valid timestamp, e.g. 2025-11-07 14:54:16")
)
