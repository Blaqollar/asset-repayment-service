//go:build integration

// Package integration exercises the service end to end: the real FX graph, the
// real HTTP server, and a real Postgres started in a container.
//
// The behaviours that matter here — idempotency and concurrent balance
// correctness — are properties of the database, not of Go code. A test that
// mocked the repository would prove only that the mock behaves as written, so
// these run against the genuine article.
package integration

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"testing"
	"time"

	"asset-repayment-service/bootstrap"
	httpx "asset-repayment-service/internal/infrastructure/handlers/http"
	"github.com/jmoiron/sqlx"
	"github.com/shopspring/decimal"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/modules/postgres"
	"github.com/testcontainers/testcontainers-go/wait"
	"go.uber.org/fx"
)

var (
	sharedDB      *sqlx.DB
	sharedBaseURL string
)

// TestMain starts one Postgres and one application instance for the whole
// package. Starting them per test would multiply a 5-second boot across every
// case for no additional confidence.
//
// The database is a throwaway container by default. Set TEST_DATABASE_URL to
// run against a Postgres you already have — the only requirement either way is
// that the database is disposable, because the suite TRUNCATEs between tests.
func TestMain(m *testing.M) {
	// run() owns the deferred cleanup; os.Exit would skip it if it were called
	// from the same function.
	os.Exit(run(m))
}

func run(m *testing.M) int {
	ctx := context.Background()

	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		container, err := postgres.Run(ctx, "postgres:16-alpine",
			postgres.WithDatabase("asset_repayment"),
			postgres.WithUsername("postgres"),
			postgres.WithPassword("postgres"),
			testcontainers.WithWaitStrategy(
				wait.ForLog("database system is ready to accept connections").
					WithOccurrence(2).
					WithStartupTimeout(90*time.Second),
			),
		)
		if err != nil {
			log.Fatalf("start postgres container: %v", err)
		}
		defer func() { _ = testcontainers.TerminateContainer(container) }()

		if dsn, err = container.ConnectionString(ctx, "sslmode=disable"); err != nil {
			log.Fatalf("container connection string: %v", err)
		}
	}

	// The application reads its configuration from the environment, so the
	// test configures it the same way a deployment would — no test-only
	// wiring, and therefore no gap between what is tested and what ships.
	os.Setenv("APP_ENV", "test")
	os.Setenv("DATABASE_URL", dsn)
	os.Setenv("HTTP_PORT", "0") // let the kernel pick a free port
	os.Setenv("REDIS_ADDRESS", "")
	os.Setenv("PAYMENT_WEBHOOK_SECRET", "")
	os.Setenv("LOG_LEVEL", "warn")

	var server *httpx.Server
	app := fx.New(bootstrap.NewApp(), fx.Populate(&sharedDB, &server))

	startCtx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()
	if err := app.Start(startCtx); err != nil {
		log.Fatalf("start application: %v", err)
	}
	defer func() {
		stopCtx, stopCancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer stopCancel()
		_ = app.Stop(stopCtx)
	}()

	sharedBaseURL = fmt.Sprintf("http://127.0.0.1:%d", server.Port())

	return m.Run()
}

// reset clears both tables so each test starts from a known ledger.
func reset(t *testing.T) {
	t.Helper()
	_, err := sharedDB.Exec(`TRUNCATE payments, deployments RESTART IDENTITY CASCADE`)
	require.NoError(t, err)
}

type apiResponse struct {
	Status  string          `json:"status"`
	Message string          `json:"message"`
	Data    json.RawMessage `json:"data"`
	Errors  map[string]any  `json:"errors"`
}

func post(t *testing.T, path string, body any) (int, apiResponse) {
	t.Helper()

	encoded, err := json.Marshal(body)
	require.NoError(t, err)

	resp, err := http.Post(sharedBaseURL+path, "application/json", bytes.NewReader(encoded))
	require.NoError(t, err)
	defer resp.Body.Close()

	return resp.StatusCode, decode(t, resp.Body)
}

// postAsync is the goroutine-safe counterpart of post. testify's require
// calls t.FailNow, which is only legal on the test's own goroutine, so the
// concurrency tests collect errors and assert on them after joining.
func postAsync(path string, body any) (int, apiResponse, error) {
	encoded, err := json.Marshal(body)
	if err != nil {
		return 0, apiResponse{}, err
	}

	resp, err := http.Post(sharedBaseURL+path, "application/json", bytes.NewReader(encoded))
	if err != nil {
		return 0, apiResponse{}, err
	}
	defer resp.Body.Close()

	var parsed apiResponse
	if err := json.NewDecoder(resp.Body).Decode(&parsed); err != nil {
		return resp.StatusCode, apiResponse{}, err
	}
	return resp.StatusCode, parsed, nil
}

// unmarshalInto is the error-returning form of unmarshalData.
func unmarshalInto(resp apiResponse, target any) error {
	if len(resp.Data) == 0 {
		return fmt.Errorf("response carried no data: %s", resp.Message)
	}
	return json.Unmarshal(resp.Data, target)
}

func get(t *testing.T, path string) (int, apiResponse) {
	t.Helper()

	resp, err := http.Get(sharedBaseURL + path)
	require.NoError(t, err)
	defer resp.Body.Close()

	return resp.StatusCode, decode(t, resp.Body)
}

func decode(t *testing.T, r io.Reader) apiResponse {
	t.Helper()

	var parsed apiResponse
	require.NoError(t, json.NewDecoder(r).Decode(&parsed))
	return parsed
}

func unmarshalData(t *testing.T, resp apiResponse, target any) {
	t.Helper()
	require.NoError(t, json.Unmarshal(resp.Data, target))
}

// seedDeployment registers a customer with the standard programme terms,
// backdated so the schedule maths has elapsed weeks to work with.
func seedDeployment(t *testing.T, customerID string, weeksAgo int) {
	t.Helper()

	status, resp := post(t, "/api/v1/deployments", map[string]any{
		"customer_id": customerID,
		"asset_id":    "AST-" + customerID,
		"principal":   "1000000",
		"term_weeks":  50,
		"start_date":  time.Now().UTC().AddDate(0, 0, -7*weeksAgo).Format("2006-01-02"),
	})
	require.Equal(t, http.StatusCreated, status, "seed failed: %s", resp.Message)
}

// notification builds the provider payload in its documented shape.
func notification(customerID, reference, amount string) map[string]any {
	return map[string]any{
		"customer_id":           customerID,
		"payment_status":        "COMPLETE",
		"transaction_amount":    amount,
		"transaction_date":      time.Now().UTC().Format("2006-01-02 15:04:05"),
		"transaction_reference": reference,
	}
}

// amountPaid reads the authoritative balance straight from the table.
func amountPaid(t *testing.T, customerID string) decimal.Decimal {
	t.Helper()

	var paid decimal.Decimal
	err := sharedDB.Get(&paid, `SELECT amount_paid FROM deployments WHERE customer_id = $1`, customerID)
	require.NoError(t, err)
	return paid
}

// requireAmount compares by value, not by string: NUMERIC(24,6) hands back
// "10000.000000" for the same money a caller writes as "10000".
func requireAmount(t *testing.T, expected string, actual decimal.Decimal, msgAndArgs ...any) {
	t.Helper()
	want, err := decimal.NewFromString(expected)
	require.NoError(t, err)
	assert.True(t, want.Equal(actual), append([]any{"expected %s, got %s", want, actual}, msgAndArgs...)...)
}

// ledgerTotal sums the applied column, which must always equal the
// materialised balance. This is the reconciliation invariant of the whole
// design: the running total is only ever a cache of the ledger.
func ledgerTotal(t *testing.T, customerID string) decimal.Decimal {
	t.Helper()

	var total decimal.Decimal
	err := sharedDB.Get(&total,
		`SELECT COALESCE(SUM(applied_amount + excess_amount), 0) FROM payments
		  WHERE customer_id = $1 AND outcome = 'applied'`, customerID)
	require.NoError(t, err)
	return total
}
