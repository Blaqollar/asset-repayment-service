package payment

import (
	"context"

	"asset-repayment-service/internal/domain/deployment"
	"github.com/google/uuid"
)

// ApplyInput is a validated credit destined for a specific deployment.
type ApplyInput struct {
	Notification ValidatedNotification
	DeploymentID int64
}

// ApplyResult is the outcome of the atomic write. 
type ApplyResult struct {
	Payment    *Payment
	Deployment *deployment.Deployment
	Duplicate  bool
}

// RecordInput persists a notification that must not move a balance.
type RecordInput struct {
	Notification   ValidatedNotification
	Outcome        string
	DeploymentID   *int64
	DeploymentUUID *uuid.UUID
}

// Repository defines payment persistence.
type Repository interface {
	Apply(ctx context.Context, input ApplyInput) (*ApplyResult, error)
	Record(ctx context.Context, input RecordInput) (stored *Payment, duplicate bool, err error)
	GetByReference(ctx context.Context, reference string) (*Payment, error)
}

// ResultCache short-circuits duplicates before they reach the database.
type ResultCache interface {
	GetResult(ctx context.Context, reference string) (*Result, bool)
	SetResult(ctx context.Context, reference string, result *Result)
}

// Queue hands an accepted notification to asynchronous processing.
type Queue interface {
	Enqueue(ctx context.Context, notification ValidatedNotification) error
}
