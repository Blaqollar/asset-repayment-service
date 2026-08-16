package repositories

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"asset-repayment-service/internal/domain/deployment"
	"asset-repayment-service/internal/infrastructure/database/models"
	"github.com/jmoiron/sqlx"
	"go.uber.org/zap"
)

// DeploymentRepository implements deployment.Repository against Postgres.
type DeploymentRepository struct {
	db     *sqlx.DB
	logger *zap.Logger
}

// NewDeploymentRepository creates the deployment repository.
func NewDeploymentRepository(db *sqlx.DB, logger *zap.Logger) deployment.Repository {
	return &DeploymentRepository{
		db:     db,
		logger: logger.With(zap.String("repository", "deployment")),
	}
}

func (r *DeploymentRepository) Create(ctx context.Context, input deployment.CreateInput) (*deployment.Deployment, error) {
	query := fmt.Sprintf(`
		INSERT INTO deployments (
			customer_id, asset_id, asset_type, virtual_account_number,
			currency, principal, term_weeks, start_date
		) VALUES ($1, $2, $3, NULLIF($4, ''), $5, $6::NUMERIC, $7, $8)
		RETURNING %s`, models.DeploymentColumns)

	var row models.DeploymentModel
	err := r.db.QueryRowxContext(ctx, query,
		input.CustomerID,
		input.AssetID,
		input.AssetType,
		input.VirtualAccountNumber,
		input.Currency.Code,
		input.Principal,
		input.TermWeeks,
		input.StartDate,
	).StructScan(&row)

	if isUniqueViolation(err) {
		return nil, deployment.ErrDuplicateActiveDeployment.WithCause(err)
	}
	if err != nil {
		return nil, fmt.Errorf("create deployment: %w", err)
	}
	return row.ToDomain(), nil
}

// GetByCustomerID returns the open deployment, or the most recent if all are
// settled.
func (r *DeploymentRepository) GetByCustomerID(ctx context.Context, customerID string) (*deployment.Deployment, error) {
	query := fmt.Sprintf(`
		SELECT %s FROM deployments
		WHERE customer_id = $1
		ORDER BY (status IN ('active', 'delinquent')) DESC, created_at DESC
		LIMIT 1`, models.DeploymentColumns)

	var row models.DeploymentModel
	if err := r.db.QueryRowxContext(ctx, query, customerID).StructScan(&row); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, deployment.ErrNotFound
		}
		return nil, fmt.Errorf("get deployment by customer: %w", err)
	}
	return row.ToDomain(), nil
}
