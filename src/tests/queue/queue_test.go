//go:build integration

// Package queue exercises the asynchronous ingest path end to end: a webhook
// call is acknowledged immediately, and the balance moves shortly afterwards in
// a worker.
//
// It runs the real application graph — the real HTTP server, the real stream
// consumer, a real Postgres — with an in-process Redis standing in for the
// broker. What is being tested is the handover, and the handover is only
// interesting if both halves are real.
package queue

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
	"asset-repayment-service/internal/application/dtos"
	httpx "asset-repayment-service/internal/infrastructure/handlers/http"
	"github.com/alicebob/miniredis/v2"
	"github.com/jmoiron/sqlx"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/modules/postgres"
	"github.com/testcontainers/testcontainers-go/wait"
	"go.uber.org/fx"
)

var (
	db      *sqlx.DB
	baseURL string
	redis   *miniredis.Miniredis
)

func TestMain(m *testing.M) { os.Exit(run(m)) }

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
					WithOccurrence(2).WithStartupTimeout(90*time.Second),
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

	var err error
	if redis, err = miniredis.Run(); err != nil {
		log.Fatalf("start miniredis: %v", err)
	}
	defer redis.Close()

	os.Setenv("APP_ENV", "test")
	os.Setenv("DATABASE_URL", dsn)
	os.Setenv("HTTP_PORT", "0")
	os.Setenv("REDIS_ADDRESS", redis.Addr())
	os.Setenv("QUEUE_ENABLED", "true")
	os.Setenv("QUEUE_STREAM", "payments:test")
	os.Setenv("QUEUE_WORKERS", "4")
	os.Setenv("QUEUE_BLOCK_TIMEOUT", "50ms")
	os.Setenv("QUEUE_CLAIM_INTERVAL", "100ms")
	os.Setenv("PAYMENT_WEBHOOK_SECRET", "")
	os.Setenv("LOG_LEVEL", "warn")

	var server *httpx.Server
	app := fx.New(bootstrap.NewApp(), fx.Populate(&db, &server))

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

	baseURL = fmt.Sprintf("http://127.0.0.1:%d", server.Port())
	return m.Run()
}

func TestWebhookIsAcknowledgedImmediatelyAndAppliedByAWorker(t *testing.T) {
	reset(t)
	seedDeployment(t, "GIG10001")

	status, resp := post(t, "/api/v1/payments", notification("GIG10001", "VPAY-ASYNC-000001", "10000"))

	// 202, not 200: the credit is safe, but the balance has not moved yet.
	require.Equal(t, http.StatusAccepted, status, resp.Message)

	var accepted dtos.ApplyPaymentResponse
	unmarshal(t, resp, &accepted)
	assert.Equal(t, "queued", accepted.Outcome)
	assert.Nil(t, accepted.Position, "a queued payment cannot quote a position it has not produced")
	require.NotNil(t, accepted.Payment)
	assert.Equal(t, "10000.00", accepted.Payment.Amount, "the accepted amount is echoed back")
	assert.Empty(t, accepted.Payment.AmountApplied, "nothing has been applied yet")

	// The worker closes the gap.
	waitForBalance(t, "GIG10001", "10000.000000")

	status, resp = get(t, "/api/v1/customers/GIG10001/position")
	require.Equal(t, http.StatusOK, status)

	var position dtos.PositionDTO
	unmarshal(t, resp, &position)
	assert.Equal(t, "10000.00", position.TotalPaid)
	assert.Equal(t, "990000.00", position.Outstanding)
	assert.Equal(t, int64(1), position.PaymentCount)
}

// The queue delivers at least once, so the ledger — not the broker — is what
// makes a duplicate harmless. This is the property the whole design rests on.
func TestRedeliveredNotificationAppliesOnce(t *testing.T) {
	reset(t)
	seedDeployment(t, "GIG10002")

	payload := notification("GIG10002", "VPAY-ASYNC-000002", "25000")
	for attempt := 0; attempt < 5; attempt++ {
		status, resp := post(t, "/api/v1/payments", payload)

		// Either answer is correct, and which one arrives is a race with the
		// worker: 202 while the credit is still queued, 200 once it has been
		// applied and the result cache can replay the original outcome. What
		// must never happen is an error, or the money moving twice.
		require.Contains(t, []int{http.StatusAccepted, http.StatusOK}, status, resp.Message)
	}

	waitForBalance(t, "GIG10002", "25000.000000")

	// Give any straggling redelivery time to arrive and be rejected by the
	// ledger rather than applied a second time.
	time.Sleep(500 * time.Millisecond)
	assert.Equal(t, "25000.000000", balance(t, "GIG10002"))

	var rows int
	require.NoError(t, db.Get(&rows,
		`SELECT count(*) FROM payments WHERE transaction_reference = $1`, "VPAY-ASYNC-000002"))
	assert.Equal(t, 1, rows, "the ledger must hold exactly one entry per reference")
}

// Bursts are the reason the queue exists: the API accepts them all at once and
// the workers drain at whatever rate the database sustains.
func TestBurstIsAcceptedAndFullyDrained(t *testing.T) {
	reset(t)
	seedDeployment(t, "GIG10003")

	const payments = 100
	for i := 0; i < payments; i++ {
		status, _ := post(t, "/api/v1/payments",
			notification("GIG10003", fmt.Sprintf("VPAY-BURST-%06d", i), "1000"))
		require.Equal(t, http.StatusAccepted, status)
	}

	waitForBalance(t, "GIG10003", "100000.000000")

	var ledger string
	require.NoError(t, db.Get(&ledger,
		`SELECT COALESCE(SUM(applied_amount + excess_amount), 0)::TEXT FROM payments
		  WHERE customer_id = $1 AND outcome = 'applied'`, "GIG10003"))
	assert.Equal(t, "100000.000000", ledger, "ledger and materialised balance must agree")
}

// A payload that cannot be understood is rejected in the request, not queued:
// the provider is the only party who can fix it, and only while it is still
// listening.
func TestMalformedPayloadIsRejectedBeforeTheQueue(t *testing.T) {
	reset(t)

	// A stream keeps acknowledged messages, so the assertion is that nothing
	// was *added* rather than that the stream is empty.
	before, err := redis.Stream("payments:test")
	require.NoError(t, err)

	status, resp := post(t, "/api/v1/payments", map[string]any{
		"customer_id":           "",
		"payment_status":        "COMPLETE",
		"transaction_amount":    "ten thousand",
		"transaction_date":      "not-a-date",
		"transaction_reference": "VPAY-ASYNC-BAD-001",
	})

	require.Equal(t, http.StatusBadRequest, status)
	assert.Contains(t, resp.Errors, "transaction_amount")

	after, err := redis.Stream("payments:test")
	require.NoError(t, err)
	assert.Len(t, after, len(before), "nothing malformed may reach the stream")
}

// --- helpers ---

type apiResponse struct {
	Status  string          `json:"status"`
	Message string          `json:"message"`
	Data    json.RawMessage `json:"data"`
	Errors  map[string]any  `json:"errors"`
}

func reset(t *testing.T) {
	t.Helper()
	_, err := db.Exec(`TRUNCATE payments, deployments RESTART IDENTITY CASCADE`)
	require.NoError(t, err)
}

func seedDeployment(t *testing.T, customerID string) {
	t.Helper()
	status, resp := post(t, "/api/v1/deployments", map[string]any{
		"customer_id": customerID,
		"asset_id":    "AST-" + customerID,
		"principal":   "1000000",
		"term_weeks":  50,
		"start_date":  time.Now().UTC().AddDate(0, 0, -70).Format("2006-01-02"),
	})
	require.Equal(t, http.StatusCreated, status, resp.Message)
}

func notification(customerID, reference, amount string) map[string]any {
	return map[string]any{
		"customer_id":           customerID,
		"payment_status":        "COMPLETE",
		"transaction_amount":    amount,
		"transaction_date":      time.Now().UTC().Format("2006-01-02 15:04:05"),
		"transaction_reference": reference,
	}
}

func balance(t *testing.T, customerID string) string {
	t.Helper()
	var paid string
	require.NoError(t, db.Get(&paid,
		`SELECT amount_paid::TEXT FROM deployments WHERE customer_id = $1`, customerID))
	return paid
}

// waitForBalance polls the authoritative balance. Asynchronous means the answer
// arrives shortly, not instantly, so the test waits for an outcome rather than
// sleeping a hopeful interval.
func waitForBalance(t *testing.T, customerID, expected string) {
	t.Helper()

	deadline := time.Now().Add(20 * time.Second)
	var last string
	for time.Now().Before(deadline) {
		if last = balance(t, customerID); last == expected {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("balance for %s never reached %s (last saw %s)", customerID, expected, last)
}

func post(t *testing.T, path string, body any) (int, apiResponse) {
	t.Helper()

	encoded, err := json.Marshal(body)
	require.NoError(t, err)

	resp, err := http.Post(baseURL+path, "application/json", bytes.NewReader(encoded))
	require.NoError(t, err)
	defer resp.Body.Close()

	return resp.StatusCode, decode(t, resp.Body)
}

func get(t *testing.T, path string) (int, apiResponse) {
	t.Helper()

	resp, err := http.Get(baseURL + path)
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

func unmarshal(t *testing.T, resp apiResponse, target any) {
	t.Helper()
	require.NoError(t, json.Unmarshal(resp.Data, target))
}
