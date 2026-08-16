package config

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
)

// isolate runs a test in an empty directory with no environment, so nothing is
// inherited from the developer's shell or the repository's .env.
func isolate(t *testing.T, settings map[string]string) {
	t.Helper()

	for _, entry := range os.Environ() {
		if key, _, found := cut(entry); found {
			t.Setenv(key, "")
			os.Unsetenv(key)
		}
	}
	for key, value := range settings {
		t.Setenv(key, value)
	}

	dir := t.TempDir()
	previous, err := os.Getwd()
	require.NoError(t, err)
	require.NoError(t, os.Chdir(dir))
	t.Cleanup(func() { _ = os.Chdir(previous) })
}

func cut(entry string) (string, string, bool) {
	for i := 0; i < len(entry); i++ {
		if entry[i] == '=' {
			return entry[:i], entry[i+1:], true
		}
	}
	return entry, "", false
}

// Nothing is defaulted in code, so an empty environment names everything it
// needs rather than starting on invented values.
func TestMissingSettingsFailWithTheFullList(t *testing.T) {
	isolate(t, nil)

	_, err := New(zap.NewNop())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "DATABASE_URL")
	assert.Contains(t, err.Error(), "HTTP_PORT")
	assert.Contains(t, err.Error(), ".env.example")
}

func TestUnreadableSettingsAreReported(t *testing.T) {
	isolate(t, complete(map[string]string{
		"HTTP_PORT":             "eight thousand",
		"HTTP_READ_TIMEOUT":     "ten seconds",
		"DATABASE_AUTO_MIGRATE": "yes please",
	}))

	_, err := New(zap.NewNop())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "HTTP_PORT (want a number)")
	assert.Contains(t, err.Error(), "HTTP_READ_TIMEOUT (want a duration such as 10s)")
	assert.Contains(t, err.Error(), "DATABASE_AUTO_MIGRATE (want true or false)")
}

// An env file is found by walking up from the working directory, which is what
// lets `go run .` and a test in a subdirectory both pick up the same settings.
func TestEnvFileIsFoundFromAParentDirectory(t *testing.T) {
	isolate(t, nil)

	root, err := os.Getwd()
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(root, ".env"), []byte(envFile(nil)), 0o600))

	nested := filepath.Join(root, "a", "b")
	require.NoError(t, os.MkdirAll(nested, 0o755))
	require.NoError(t, os.Chdir(nested))

	cfg, err := New(zap.NewNop())
	require.NoError(t, err)
	assert.Equal(t, 8080, cfg.HTTP.Port)
}

// The environment always wins over the file, which is how a container overrides
// what a developer's copy says.
func TestEnvironmentWinsOverTheFile(t *testing.T) {
	isolate(t, map[string]string{"HTTP_PORT": "9999"})

	root, err := os.Getwd()
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(root, ".env"), []byte(envFile(nil)), 0o600))

	cfg, err := New(zap.NewNop())
	require.NoError(t, err)
	assert.Equal(t, 9999, cfg.HTTP.Port)
}

func TestProductionRefusesUnsignedPayments(t *testing.T) {
	isolate(t, complete(map[string]string{"APP_ENV": "production"}))

	_, err := New(zap.NewNop())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "PAYMENT_WEBHOOK_SECRET")

	t.Setenv("PAYMENT_WEBHOOK_SECRET", "s3cret")
	cfg, err := New(zap.NewNop())
	require.NoError(t, err)
	assert.True(t, cfg.SignatureVerificationEnabled())
}

func TestOptionalSettingsMayBeEmptyButNotAbsent(t *testing.T) {
	settings := complete(nil)
	isolate(t, settings)

	cfg, err := New(zap.NewNop())
	require.NoError(t, err)
	assert.False(t, cfg.RedisEnabled(), "an empty REDIS_ADDRESS means no cache")
	assert.False(t, cfg.QueueEnabled(), "the queue needs a broker")

	os.Unsetenv("REDIS_ADDRESS")
	_, err = New(zap.NewNop())
	require.Error(t, err, "absent is a mistake even where empty is a choice")
	assert.Contains(t, err.Error(), "REDIS_ADDRESS")
}

// settings is a complete, valid environment; tests override single keys of it.
func settings() map[string]string {
	return map[string]string{
		"APP_NAME": "asset-repayment-service", "APP_ENV": "test",
		"HTTP_PORT": "8080", "HTTP_READ_TIMEOUT": "10s", "HTTP_WRITE_TIMEOUT": "15s",
		"HTTP_IDLE_TIMEOUT": "90s", "HTTP_SHUTDOWN_TIMEOUT": "20s",
		"HTTP_MAX_BODY_BYTES": "16384", "HTTP_REQUEST_TIMEOUT": "5s",
		"HTTP_MAX_INFLIGHT_PAYMENTS": "1024",
		"DATABASE_URL":               "postgres://localhost:5432/asset_repayment?sslmode=disable",
		"DATABASE_MAX_OPEN_CONNS":    "25", "DATABASE_MAX_IDLE_CONNS": "25",
		"DATABASE_CONN_MAX_LIFETIME": "30m", "DATABASE_CONN_MAX_IDLE_TIME": "5m",
		"DATABASE_AUTO_MIGRATE": "true",
		"REDIS_ADDRESS":         "", "REDIS_PASSWORD": "", "REDIS_DB": "0",
		"REDIS_POOL_SIZE": "64", "REDIS_TIMEOUT": "50ms",
		"REDIS_REF_TTL": "30m", "REDIS_RESULT_TTL": "24h",
		"QUEUE_ENABLED": "false", "QUEUE_STREAM": "payments:inbound",
		"QUEUE_GROUP": "payment-workers", "QUEUE_WORKERS": "16",
		"QUEUE_BATCH_SIZE": "64", "QUEUE_BLOCK_TIMEOUT": "2s",
		"QUEUE_PROCESS_TIMEOUT": "10s", "QUEUE_TIMEOUT": "2s",
		"QUEUE_CLAIM_MIN_IDLE": "30s", "QUEUE_CLAIM_INTERVAL": "15s",
		"QUEUE_MAX_ATTEMPTS": "5", "QUEUE_MAX_LEN": "1000000",
		"PAYMENT_WEBHOOK_SECRET": "", "PAYMENT_SIGNATURE_HEADER": "X-Payment-Signature",
		"PAYMENT_PROVIDER_TIMEZONE": "Africa/Lagos", "PAYMENT_CURRENCY": "NGN",
		"PAYMENT_DEFAULT_PRINCIPAL": "1000000", "PAYMENT_DEFAULT_TERM_WEEKS": "50",
		"METRICS_ENABLED": "true",
	}
}

func complete(overrides map[string]string) map[string]string {
	out := settings()
	for key, value := range overrides {
		out[key] = value
	}
	return out
}

func envFile(overrides map[string]string) string {
	var b []byte
	for key, value := range complete(overrides) {
		b = append(b, (key + "=" + value + "\n")...)
	}
	return string(b)
}
