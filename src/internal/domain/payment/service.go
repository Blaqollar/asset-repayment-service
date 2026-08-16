package payment

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"asset-repayment-service/internal/domain/deployment"
	pkgerrors "asset-repayment-service/pkg/errors"
	"asset-repayment-service/pkg/money"
	"go.uber.org/zap"
)

// Settings are the environment-derived values the domain needs to operate.
type Settings struct {
	ProviderLocation *time.Location
	DefaultCurrency money.Currency
}

// Result is the outcome of one notification.
type Result struct {
	Outcome  string
	Payment  *Payment
	Position *deployment.Position
}

// Applied reports whether the credit reduced an outstanding balance.
func (r *Result) Applied() bool { return r.Outcome == OutcomeApplied }

// Service defines payment domain behaviour.
type Service interface {
	Accept(ctx context.Context, notification Notification, raw json.RawMessage) (*Result, error)
	Process(ctx context.Context, validated ValidatedNotification) (*Result, error)
}

type service struct {
	repository  Repository
	deployments deployment.Service
	cache       ResultCache
	queue       Queue
	settings    Settings
	logger      *zap.Logger
}

// NewService creates the payment domain service.
func NewService(
	repository Repository,
	deployments deployment.Service,
	cache ResultCache,
	queue Queue,
	settings Settings,
	logger *zap.Logger,
) Service {
	return &service{
		repository:  repository,
		deployments: deployments,
		cache:       cache,
		queue:       queue,
		settings:    settings,
		logger:      logger.With(zap.String("service", "payment")),
	}
}

// Accept is the webhook's entry point.
func (s *service) Accept(ctx context.Context, notification Notification, raw json.RawMessage) (*Result, error) {
	validated, err := notification.Validate(s.settings.ProviderLocation, s.settings.DefaultCurrency, raw)
	if err != nil {
		return nil, err
	}

	// Answer a settled reference from cache, applying the same conflict check
	// as the database path — otherwise the cache would be a hole in it.
	if cached, ok := s.cache.GetResult(ctx, validated.Reference); ok {
		if err := sameRequest(cached.Payment, *validated); err != nil {
			return nil, err
		}
		replay := *cached
		replay.Outcome = OutcomeDuplicate
		return &replay, nil
	}

	if s.queue == nil {
		return s.Process(ctx, *validated)
	}

	if err := s.queue.Enqueue(ctx, *validated); err != nil {
		// A credit is never refused because a broker is down.
		s.logger.Warn("queue unavailable, applying payment inline",
			zap.String("transaction_reference", validated.Reference),
			zap.Error(err),
		)
		return s.Process(ctx, *validated)
	}

	return &Result{Outcome: OutcomeQueued, Payment: pendingPayment(*validated)}, nil
}

// pendingPayment echoes back an accepted credit. 
func pendingPayment(v ValidatedNotification) *Payment {
	return &Payment{
		TransactionReference: v.Reference,
		CustomerID:           v.CustomerID,
		Currency:             v.Currency,
		Amount:               v.Amount,
		Outcome:              OutcomeQueued,
		ProviderStatus:       v.ProviderStatus,
		TransactionDate:      v.TransactionDate,
		ReceivedAt:           v.ReceivedAt,
	}
}

// Process moves the money.
func (s *service) Process(ctx context.Context, v ValidatedNotification) (*Result, error) {
	validated := &v

	// Routing precedes the status check so an unsettled notification is still
	// filed against the deployment it belongs to.
	ref, err := s.deployments.Resolve(ctx, validated.CustomerID)
	if err != nil {
		if isRoutingFailure(err) {
			s.logger.Warn("payment could not be routed to a deployment",
				zap.String("customer_id", validated.CustomerID),
				zap.String("transaction_reference", validated.Reference),
				zap.Error(err),
			)
			return s.record(ctx, *validated, OutcomeUnmatched, nil)
		}
		return nil, err
	}

	// Funds that have not settled are recorded but never applied.
	if !IsSuccessful(validated.ProviderStatus) {
		return s.record(ctx, *validated, OutcomeIgnored, &ref.ID)
	}

	applied, err := s.repository.Apply(ctx, ApplyInput{Notification: *validated, DeploymentID: ref.ID})
	if err != nil {
		return nil, err
	}

	result := &Result{Outcome: OutcomeApplied, Payment: applied.Payment}
	if applied.Duplicate {
		if err := sameRequest(applied.Payment, *validated); err != nil {
			return nil, err
		}
		result.Outcome = OutcomeDuplicate
	}
	if applied.Deployment != nil {
		position := applied.Deployment.PositionAt(time.Now().UTC())
		result.Position = &position
	}

	if !applied.Duplicate {
		s.cache.SetResult(ctx, validated.Reference, result)
	}
	return result, nil
}

// record persists a notification that must not move a balance.
func (s *service) record(ctx context.Context, validated ValidatedNotification, outcome string, deploymentID *int64) (*Result, error) {
	stored, duplicate, err := s.repository.Record(ctx, RecordInput{
		Notification: validated,
		Outcome:      outcome,
		DeploymentID: deploymentID,
	})
	if err != nil {
		return nil, err
	}

	if duplicate {
		if err := sameRequest(stored, validated); err != nil {
			return nil, err
		}
		return &Result{Outcome: OutcomeDuplicate, Payment: stored}, nil
	}
	return &Result{Outcome: outcome, Payment: stored}, nil
}

// sameRequest guards the replay path. 
func sameRequest(stored *Payment, v ValidatedNotification) error {
	switch {
	case stored == nil:
		return nil
	case stored.CustomerID != v.CustomerID:
		return ErrReferenceConflict.WithDetails(map[string]any{
			"customer_id": "does not match the customer this reference was recorded against",
		})
	case !stored.Amount.Equal(v.Amount):
		return ErrReferenceConflict.WithDetails(map[string]any{
			"transaction_amount": "does not match the amount recorded for this reference",
		})
	case stored.Currency.Code != v.Currency.Code:
		return ErrReferenceConflict.WithDetails(map[string]any{
			"currency": "does not match the currency recorded for this reference",
		})
	}
	return nil
}

// isRoutingFailure separates "nowhere to apply this money" (record it as
// unmatched) from infrastructure failure (propagate, so the provider retries).
func isRoutingFailure(err error) bool {
	if errors.Is(err, deployment.ErrNotFound) || errors.Is(err, deployment.ErrNoOpenDeployment) {
		return true
	}

	var domainErr *pkgerrors.DomainError
	if errors.As(err, &domainErr) {
		return domainErr.Code == pkgerrors.CodeNotFound || domainErr.Code == pkgerrors.CodeFailedPrecondition
	}
	return false
}
