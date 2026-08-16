package database

import (
	"context"
	"embed"
	"errors"
	"fmt"
	"time"

	"asset-repayment-service/internal/config"
	"github.com/golang-migrate/migrate/v4"
	migratepostgres "github.com/golang-migrate/migrate/v4/database/postgres"
	"github.com/golang-migrate/migrate/v4/source/iofs"
	"github.com/jmoiron/sqlx"
	"go.uber.org/fx"
	"go.uber.org/zap"

	_ "github.com/jackc/pgx/v5/stdlib"
)

// Migrations are embedded so a misconfigured volume mount cannot start the
// service against an unmigrated schema.
//
//go:embed migrations/*.sql
var migrationFS embed.FS

const driverName = "pgx"

type DatabaseParams struct {
	fx.In

	Config *config.Config
	Logger *zap.Logger
}

// New opens the connection pool and applies migrations on start.
func New(params DatabaseParams, lc fx.Lifecycle) (*sqlx.DB, error) {
	logger := params.Logger.With(zap.String("module", "database"))

	db, err := sqlx.Open(driverName, params.Config.Database.URL)
	if err != nil {
		return nil, fmt.Errorf("open database: %w", err)
	}

	// Sized for a fleet, not one pod.
	db.SetMaxOpenConns(params.Config.Database.MaxOpenConns)
	db.SetMaxIdleConns(params.Config.Database.MaxIdleConns)
	db.SetConnMaxLifetime(params.Config.Database.ConnMaxLifetime)
	db.SetConnMaxIdleTime(params.Config.Database.ConnMaxIdleTime)

	lc.Append(fx.Hook{
		OnStart: func(ctx context.Context) error {
			pingCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
			defer cancel()

			if err := db.PingContext(pingCtx); err != nil {
				_ = db.Close()
				return fmt.Errorf("ping database: %w", err)
			}
			logger.Info("database connection established",
				zap.Int("max_open_conns", params.Config.Database.MaxOpenConns),
			)

			if !params.Config.Database.AutoMigrate {
				logger.Info("automatic migrations disabled")
				return nil
			}
			return RunMigrations(db, logger)
		},
		OnStop: func(context.Context) error {
			logger.Debug("closing database connection")
			return db.Close()
		},
	})

	return db, nil
}

// Module wires the database into the FX graph.
var Module = fx.Module("database", fx.Provide(New))

// RunMigrations applies all pending migrations.
func RunMigrations(db *sqlx.DB, logger *zap.Logger) error {
	source, err := iofs.New(migrationFS, "migrations")
	if err != nil {
		return fmt.Errorf("load embedded migrations: %w", err)
	}

	driver, err := migratepostgres.WithInstance(db.DB, &migratepostgres.Config{})
	if err != nil {
		return fmt.Errorf("init migration driver: %w", err)
	}

	migrator, err := migrate.NewWithInstance("iofs", source, "postgres", driver)
	if err != nil {
		return fmt.Errorf("init migrator: %w", err)
	}

	if err := migrator.Up(); err != nil && !errors.Is(err, migrate.ErrNoChange) {
		return fmt.Errorf("apply migrations: %w", err)
	}

	version, dirty, err := migrator.Version()
	if err != nil && !errors.Is(err, migrate.ErrNilVersion) {
		return fmt.Errorf("read migration version: %w", err)
	}
	logger.Info("migrations applied", zap.Uint("version", version), zap.Bool("dirty", dirty))
	return nil
}
