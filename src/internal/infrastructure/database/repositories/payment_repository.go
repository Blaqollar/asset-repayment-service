package repositories

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"

	"asset-repayment-service/internal/domain/deployment"
	"asset-repayment-service/internal/domain/payment"
	"asset-repayment-service/internal/infrastructure/database/models"
	"asset-repayment-service/pkg/money"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jmoiron/sqlx"
	"go.uber.org/zap"
)

// uniqueViolation is the SQLSTATE that means this reference was already used.
const uniqueViolation = "23505"

// applyPaymentQuery advances the balance and appends the ledger entry in one
// statement
var applyPaymentQuery = fmt.Sprintf(`
WITH moved AS (
	UPDATE deployments d
	   SET amount_paid     = d.amount_paid + $4::NUMERIC,
	       payment_count   = d.payment_count + 1,
	       last_payment_at = GREATEST(d.last_payment_at, $6::TIMESTAMPTZ),
	       status = CASE
	                  WHEN d.amount_paid + $4::NUMERIC >= d.principal THEN 'completed'
	                  ELSE d.status
	                END,
	       completed_at = CASE
	                  WHEN d.amount_paid + $4::NUMERIC >= d.principal
	                       AND d.completed_at IS NULL THEN NOW()
	                  ELSE d.completed_at
	                END,
	       updated_at = NOW()
	 WHERE d.id = $3::BIGINT
	   AND NOT EXISTS (
	       SELECT 1 FROM payments p WHERE p.transaction_reference = $1
	   )
	RETURNING %s
),
split AS MATERIALIZED (
	SELECT m.*,
	       LEAST(
	           $4::NUMERIC,
	           GREATEST(m.principal - (m.amount_paid - $4::NUMERIC), 0)
	       ) AS applied_amount
	  FROM moved m
),
recorded AS (
	INSERT INTO payments (
		transaction_reference, customer_id, deployment_id, currency, amount,
		applied_amount, excess_amount, balance_before, balance_after,
		outcome, provider_status, transaction_date, received_at, raw_payload
	)
	SELECT $1, $2, s.id, s.currency, $4::NUMERIC,
	       s.applied_amount,
	       $4::NUMERIC - s.applied_amount,
	       s.amount_paid - $4::NUMERIC,
	       s.amount_paid,
	       'applied', $5, $6::TIMESTAMPTZ, $7::TIMESTAMPTZ, $8::JSONB
	  FROM split s
	RETURNING uuid, applied_amount, excess_amount, balance_before, balance_after, created_at
)
SELECT r.uuid              AS payment_uuid,
       r.applied_amount,
       r.excess_amount,
       r.balance_before,
       r.balance_after,
       r.created_at        AS payment_created_at,
       s.id, s.uuid, s.customer_id, s.asset_id, s.asset_type, s.virtual_account_number,
       s.currency, s.principal, s.term_weeks, s.amount_paid, s.payment_count, s.status,
       s.start_date, s.last_payment_at, s.completed_at, s.created_at, s.updated_at
  FROM recorded r
 CROSS JOIN split s`, qualifiedDeploymentColumns)

// qualifiedDeploymentColumns is the projection aliased for UPDATE ... RETURNING.
const qualifiedDeploymentColumns = `d.id, d.uuid, d.customer_id, d.asset_id, d.asset_type,
	d.virtual_account_number, d.currency, d.principal, d.term_weeks, d.amount_paid,
	d.payment_count, d.status, d.start_date, d.last_payment_at, d.completed_at,
	d.created_at, d.updated_at`

// PaymentRepository implements payment.Repository against Postgres.
type PaymentRepository struct {
	db     *sqlx.DB
	logger *zap.Logger
}

// NewPaymentRepository creates the payment repository.
func NewPaymentRepository(db *sqlx.DB, logger *zap.Logger) payment.Repository {
	return &PaymentRepository{
		db:     db,
		logger: logger.With(zap.String("repository", "payment")),
	}
}

// Apply records the payment and advances the balance atomically.
func (r *PaymentRepository) Apply(ctx context.Context, input payment.ApplyInput) (*payment.ApplyResult, error) {
	n := input.Notification

	var row models.ApplyRowModel
	err := r.db.QueryRowxContext(ctx, applyPaymentQuery,
		n.Reference,
		n.CustomerID,
		input.DeploymentID,
		n.Amount,
		n.ProviderStatus,
		n.TransactionDate,
		n.ReceivedAt,
		rawPayloadOrEmpty(n.RawPayload),
	).StructScan(&row)

	switch {
	case err == nil:
		return &payment.ApplyResult{
			Payment:    applyRowToPayment(row, n),
			Deployment: row.DeploymentModel.ToDomain(),
			Duplicate:  false,
		}, nil

	case errors.Is(err, sql.ErrNoRows):
		// The probe matched: this reference was processed earlier.
		return r.replay(ctx, input)

	case isUniqueViolation(err):
		// Two copies raced; this one lost and its balance update rolled back.
		r.logger.Debug("concurrent duplicate payment rejected by unique constraint",
			zap.String("transaction_reference", n.Reference),
		)
		return r.replay(ctx, input)

	default:
		return nil, fmt.Errorf("apply payment: %w", err)
	}
}

// replay reconstructs the original outcome, so a retry gets the same answer.
func (r *PaymentRepository) replay(ctx context.Context, input payment.ApplyInput) (*payment.ApplyResult, error) {
	stored, err := r.GetByReference(ctx, input.Notification.Reference)
	if err != nil {
		return nil, err
	}
	if stored == nil {
		// The deployment vanished between routing and writing; surfacing the
		// error lets the provider retry rather than losing the credit.
		return nil, deployment.ErrNotFound
	}

	current, err := r.deploymentByID(ctx, input.DeploymentID)
	if err != nil {
		return nil, err
	}
	return &payment.ApplyResult{Payment: stored, Deployment: current, Duplicate: true}, nil
}

// Record persists a notification that must not move any balance.
func (r *PaymentRepository) Record(ctx context.Context, input payment.RecordInput) (*payment.Payment, bool, error) {
	n := input.Notification

	query := fmt.Sprintf(`
		INSERT INTO payments (
			transaction_reference, customer_id, deployment_id, currency, amount,
			applied_amount, excess_amount, balance_before, balance_after,
			outcome, provider_status, transaction_date, received_at, raw_payload
		) VALUES ($1, $2, $3, $4, $5::NUMERIC, 0, 0, 0, 0, $6, $7, $8::TIMESTAMPTZ, $9::TIMESTAMPTZ, $10::JSONB)
		ON CONFLICT (transaction_reference) DO NOTHING
		RETURNING %s`, models.PaymentColumns)

	var row models.PaymentModel
	err := r.db.QueryRowxContext(ctx, query,
		n.Reference,
		n.CustomerID,
		input.DeploymentID,
		n.Currency.Code,
		n.Amount,
		input.Outcome,
		n.ProviderStatus,
		n.TransactionDate,
		n.ReceivedAt,
		rawPayloadOrEmpty(n.RawPayload),
	).StructScan(&row)

	if errors.Is(err, sql.ErrNoRows) {
		stored, lookupErr := r.GetByReference(ctx, n.Reference)
		if lookupErr != nil {
			return nil, false, lookupErr
		}
		return stored, true, nil
	}
	if err != nil {
		return nil, false, fmt.Errorf("record payment: %w", err)
	}
	return row.ToDomain(), false, nil
}

func (r *PaymentRepository) GetByReference(ctx context.Context, reference string) (*payment.Payment, error) {
	query := fmt.Sprintf(`SELECT %s FROM payments WHERE transaction_reference = $1`, models.PaymentColumns)

	var row models.PaymentModel
	if err := r.db.QueryRowxContext(ctx, query, reference).StructScan(&row); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil
		}
		return nil, fmt.Errorf("get payment by reference: %w", err)
	}
	return row.ToDomain(), nil
}

func (r *PaymentRepository) deploymentByID(ctx context.Context, id int64) (*deployment.Deployment, error) {
	query := fmt.Sprintf(`SELECT %s FROM deployments WHERE id = $1`, models.DeploymentColumns)

	var row models.DeploymentModel
	if err := r.db.QueryRowxContext(ctx, query, id).StructScan(&row); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, deployment.ErrNotFound
		}
		return nil, fmt.Errorf("get deployment by id: %w", err)
	}
	return row.ToDomain(), nil
}

func applyRowToPayment(row models.ApplyRowModel, n payment.ValidatedNotification) *payment.Payment {
	deploymentID := row.DeploymentModel.ID
	deploymentUUID := row.DeploymentModel.UUID

	return &payment.Payment{
		UUID:                 row.PaymentUUID,
		TransactionReference: n.Reference,
		CustomerID:           n.CustomerID,
		DeploymentID:         &deploymentID,
		DeploymentUUID:       &deploymentUUID,
		// The deployment's currency: what the money actually landed in.
		Currency:        money.Lookup(row.DeploymentModel.Currency),
		Amount:          n.Amount,
		Applied:         row.AppliedAmount,
		Excess:          row.ExcessAmount,
		BalanceBefore:   row.BalanceBefore,
		BalanceAfter:    row.BalanceAfter,
		Outcome:         payment.OutcomeApplied,
		ProviderStatus:  n.ProviderStatus,
		TransactionDate: n.TransactionDate,
		ReceivedAt:      n.ReceivedAt,
		CreatedAt:       row.PaymentCreatedAt.UTC(),
	}
}

// rawPayloadOrEmpty guarantees valid JSON for the NOT NULL jsonb column;
// callers off the HTTP path have no verbatim body to retain.
func rawPayloadOrEmpty(raw json.RawMessage) []byte {
	if len(raw) == 0 {
		return []byte("{}")
	}
	return raw
}

// isUniqueViolation reports a Postgres unique constraint breach.
func isUniqueViolation(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == uniqueViolation
}
