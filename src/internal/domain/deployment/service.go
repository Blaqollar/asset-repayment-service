package deployment

import (
	"context"
	"strings"
	"time"

	"asset-repayment-service/pkg/money"
)

// maxTermWeeks bounds the repayment term at ten years.
const maxTermWeeks = 520

// Defaults are the standard programme terms
type Defaults struct {
	Currency  money.Currency
	Principal money.Amount
	TermWeeks int
}

// Service defines deployment domain behaviour.
type Service interface {
	Create(ctx context.Context, input CreateInput) (*Deployment, error)
	Resolve(ctx context.Context, customerID string) (*Ref, error)
	GetPosition(ctx context.Context, customerID string, asOf time.Time) (*Position, error)
	Defaults() Defaults
}

type service struct {
	repository Repository
	cache      RefCache
	defaults   Defaults
}

// NewService creates the deployment domain service.
func NewService(repository Repository, cache RefCache, defaults Defaults) Service {
	return &service{repository: repository, cache: cache, defaults: defaults}
}

func (s *service) Create(ctx context.Context, input CreateInput) (*Deployment, error) {
	input.CustomerID = strings.TrimSpace(input.CustomerID)
	if input.CustomerID == "" {
		return nil, ErrInvalidCustomerID
	}

	if input.Currency.Code == "" {
		input.Currency = s.defaults.Currency
	}
	if input.Principal.IsZero() {
		input.Principal = s.defaults.Principal
	}
	if input.TermWeeks == 0 {
		input.TermWeeks = s.defaults.TermWeeks
	}

	if !input.Principal.IsPositive() {
		return nil, ErrInvalidPrincipal
	}
	if input.TermWeeks < 0 || input.TermWeeks > maxTermWeeks {
		return nil, ErrInvalidTerm
	}
	if input.StartDate.IsZero() {
		input.StartDate = time.Now().UTC()
	}
	input.StartDate = input.StartDate.UTC().Truncate(24 * time.Hour)

	created, err := s.repository.Create(ctx, input)
	if err != nil {
		return nil, err
	}

	s.cache.SetRef(ctx, created.ToRef())
	return created, nil
}

func (s *service) Defaults() Defaults { return s.defaults }

// Resolve is on the hot path of every payment
func (s *service) Resolve(ctx context.Context, customerID string) (*Ref, error) {
	customerID = strings.TrimSpace(customerID)
	if customerID == "" {
		return nil, ErrInvalidCustomerID
	}

	if ref, ok := s.cache.GetRef(ctx, customerID); ok {
		return ref, nil
	}

	found, err := s.repository.GetByCustomerID(ctx, customerID)
	if err != nil {
		return nil, err
	}
	// Money can only be applied to a deployment still accepting repayments.
	if !found.IsOpen() {
		return nil, ErrNoOpenDeployment
	}

	ref := found.ToRef()
	s.cache.SetRef(ctx, ref)
	return &ref, nil
}

// GetPosition always reads through to the database, because a stale position
// misreports a debt.
func (s *service) GetPosition(ctx context.Context, customerID string, asOf time.Time) (*Position, error) {
	found, err := s.repository.GetByCustomerID(ctx, strings.TrimSpace(customerID))
	if err != nil {
		return nil, err
	}
	if asOf.IsZero() {
		asOf = time.Now().UTC()
	}

	position := found.PositionAt(asOf)
	return &position, nil
}
