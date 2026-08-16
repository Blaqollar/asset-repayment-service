package config

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"asset-repayment-service/internal/domain/deployment"
	"asset-repayment-service/internal/domain/payment"
	"asset-repayment-service/pkg/money"
	"github.com/joho/godotenv"
	"go.uber.org/fx"
	"go.uber.org/zap"
)

type Config struct {
	App struct {
		Name string
		Env  string
	}

	HTTP struct {
		Port                int
		ReadTimeout         time.Duration
		WriteTimeout        time.Duration
		IdleTimeout         time.Duration
		ShutdownTimeout     time.Duration
		MaxBodyBytes        int64
		RequestTimeout      time.Duration
		MaxInflightPayments int
	}

	Database struct {
		URL             string
		MaxOpenConns    int
		MaxIdleConns    int
		ConnMaxLifetime time.Duration
		ConnMaxIdleTime time.Duration
		AutoMigrate     bool
	}

	Redis struct {
		Address   string
		Password  string
		DB        int
		PoolSize  int
		Timeout   time.Duration
		RefTTL    time.Duration
		ResultTTL time.Duration
	}

	// Queue is the optional asynchronous ingest path, off by default.
	Queue struct {
		Enabled        bool
		Stream         string
		Group          string
		Workers        int
		BatchSize      int64
		BlockTimeout   time.Duration
		ProcessTimeout time.Duration
		Timeout        time.Duration
		ClaimMinIdle   time.Duration
		ClaimInterval  time.Duration
		MaxAttempts    int64
		MaxLen         int64
	}

	Payments struct {
		WebhookSecret    string
		SignatureHeader  string
		ProviderTimezone string
		Currency         money.Currency
		DefaultPrincipal money.Amount
		DefaultTermWeeks int

		providerLocation *time.Location
	}

	Observability struct {
		MetricsEnabled bool
	}
}

// New loads configuration from the environment.
func New(logger *zap.Logger) (*Config, error) {
	if file, err := loadEnvFile(); err != nil {
		logger.Warn("could not read env file; continuing with the environment", zap.Error(err))
	} else if file != "" {
		logger.Info("loaded env file", zap.String("path", file))
	}

	env := &reader{}
	cfg := &Config{}

	cfg.App.Name = env.str("APP_NAME")
	cfg.App.Env = env.str("APP_ENV")

	cfg.HTTP.Port = env.int("HTTP_PORT")
	cfg.HTTP.ReadTimeout = env.duration("HTTP_READ_TIMEOUT")
	cfg.HTTP.WriteTimeout = env.duration("HTTP_WRITE_TIMEOUT")
	cfg.HTTP.IdleTimeout = env.duration("HTTP_IDLE_TIMEOUT")
	cfg.HTTP.ShutdownTimeout = env.duration("HTTP_SHUTDOWN_TIMEOUT")
	cfg.HTTP.MaxBodyBytes = int64(env.int("HTTP_MAX_BODY_BYTES"))
	cfg.HTTP.MaxInflightPayments = env.int("HTTP_MAX_INFLIGHT_PAYMENTS")
	cfg.HTTP.RequestTimeout = env.duration("HTTP_REQUEST_TIMEOUT")

	cfg.Database.URL = env.str("DATABASE_URL")
	cfg.Database.MaxOpenConns = env.int("DATABASE_MAX_OPEN_CONNS")
	cfg.Database.MaxIdleConns = env.int("DATABASE_MAX_IDLE_CONNS")
	cfg.Database.ConnMaxLifetime = env.duration("DATABASE_CONN_MAX_LIFETIME")
	cfg.Database.ConnMaxIdleTime = env.duration("DATABASE_CONN_MAX_IDLE_TIME")
	cfg.Database.AutoMigrate = env.bool("DATABASE_AUTO_MIGRATE")

	// Redis is optional
	cfg.Redis.Address = env.optional("REDIS_ADDRESS")
	cfg.Redis.Password = env.optional("REDIS_PASSWORD")
	cfg.Redis.DB = env.int("REDIS_DB")
	cfg.Redis.PoolSize = env.int("REDIS_POOL_SIZE")
	cfg.Redis.Timeout = env.duration("REDIS_TIMEOUT")
	cfg.Redis.RefTTL = env.duration("REDIS_REF_TTL")
	cfg.Redis.ResultTTL = env.duration("REDIS_RESULT_TTL")

	cfg.Queue.Enabled = env.bool("QUEUE_ENABLED")
	cfg.Queue.Stream = env.str("QUEUE_STREAM")
	cfg.Queue.Group = env.str("QUEUE_GROUP")
	cfg.Queue.Workers = env.int("QUEUE_WORKERS")
	cfg.Queue.BatchSize = int64(env.int("QUEUE_BATCH_SIZE"))
	cfg.Queue.BlockTimeout = env.duration("QUEUE_BLOCK_TIMEOUT")
	cfg.Queue.ProcessTimeout = env.duration("QUEUE_PROCESS_TIMEOUT")
	cfg.Queue.Timeout = env.duration("QUEUE_TIMEOUT")
	cfg.Queue.ClaimMinIdle = env.duration("QUEUE_CLAIM_MIN_IDLE")
	cfg.Queue.ClaimInterval = env.duration("QUEUE_CLAIM_INTERVAL")
	cfg.Queue.MaxAttempts = int64(env.int("QUEUE_MAX_ATTEMPTS"))
	cfg.Queue.MaxLen = int64(env.int("QUEUE_MAX_LEN"))

	cfg.Payments.WebhookSecret = env.optional("PAYMENT_WEBHOOK_SECRET")
	cfg.Payments.SignatureHeader = env.str("PAYMENT_SIGNATURE_HEADER")
	cfg.Payments.Currency = money.Lookup(env.str("PAYMENT_CURRENCY"))
	cfg.Payments.DefaultTermWeeks = env.int("PAYMENT_DEFAULT_TERM_WEEKS")
	cfg.Payments.ProviderTimezone = env.str("PAYMENT_PROVIDER_TIMEZONE")

	cfg.Observability.MetricsEnabled = env.bool("METRICS_ENABLED")

	if err := env.err(); err != nil {
		return nil, err
	}

	principal, err := money.ParsePositive(env.str("PAYMENT_DEFAULT_PRINCIPAL"), cfg.Payments.Currency)
	if err != nil {
		return nil, fmt.Errorf("invalid PAYMENT_DEFAULT_PRINCIPAL: %w", err)
	}
	cfg.Payments.DefaultPrincipal = principal

	location, err := time.LoadLocation(cfg.Payments.ProviderTimezone)
	if err != nil {
		return nil, fmt.Errorf("invalid PAYMENT_PROVIDER_TIMEZONE %q: %w", cfg.Payments.ProviderTimezone, err)
	}
	cfg.Payments.providerLocation = location

	if err := cfg.Validate(); err != nil {
		return nil, err
	}

	logger.Info("configuration loaded",
		zap.String("env", cfg.App.Env),
		zap.Int("http_port", cfg.HTTP.Port),
		zap.Bool("cache_enabled", cfg.RedisEnabled()),
		zap.Bool("queue_enabled", cfg.QueueEnabled()),
		zap.String("currency", cfg.Payments.Currency.Code),
		zap.String("provider_timezone", cfg.Payments.ProviderTimezone),
	)
	return cfg, nil
}

var envFiles = []string{".env", ".env.example"}

// loadEnvFile loads the first env file found
func loadEnvFile() (string, error) {
	dir, err := os.Getwd()
	if err != nil {
		return "", err
	}

	for {
		for _, name := range envFiles {
			path := filepath.Join(dir, name)
			if _, err := os.Stat(path); err == nil {
				return path, godotenv.Load(path)
			}
		}

		parent := filepath.Dir(dir)
		if parent == dir {
			return "", nil
		}
		dir = parent
	}
}

// reader pulls settings out of the environment.
type reader struct {
	missing []string
	invalid []string
}

// str returns a setting that must be present and non-empty.
func (r *reader) str(key string) string {
	value, ok := os.LookupEnv(key)
	value = strings.TrimSpace(value)
	if !ok || value == "" {
		r.missing = append(r.missing, key)
	}
	return value
}

// optional returns a setting that must be present but may be empty.
func (r *reader) optional(key string) string {
	value, ok := os.LookupEnv(key)
	if !ok {
		r.missing = append(r.missing, key)
	}
	return strings.TrimSpace(value)
}

func (r *reader) int(key string) int {
	raw := r.str(key)
	if raw == "" {
		return 0
	}
	parsed, err := strconv.Atoi(raw)
	if err != nil {
		r.invalid = append(r.invalid, key+" (want a number)")
	}
	return parsed
}

func (r *reader) bool(key string) bool {
	raw := r.str(key)
	if raw == "" {
		return false
	}
	parsed, err := strconv.ParseBool(raw)
	if err != nil {
		r.invalid = append(r.invalid, key+" (want true or false)")
	}
	return parsed
}

func (r *reader) duration(key string) time.Duration {
	raw := r.str(key)
	if raw == "" {
		return 0
	}
	parsed, err := time.ParseDuration(raw)
	if err != nil {
		r.invalid = append(r.invalid, key+" (want a duration such as 10s)")
	}
	return parsed
}

func (r *reader) err() error {
	var problems []string
	if len(r.missing) > 0 {
		problems = append(problems, "missing: "+strings.Join(r.missing, ", "))
	}
	if len(r.invalid) > 0 {
		problems = append(problems, "invalid: "+strings.Join(r.invalid, ", "))
	}
	if len(problems) == 0 {
		return nil
	}
	return errors.New("configuration " + strings.Join(problems, "; ") + " — see .env.example")
}

// RedisEnabled reports whether a cache backend is configured.
func (c *Config) RedisEnabled() bool { return c.Redis.Address != "" }

// QueueEnabled reports whether payments are processed asynchronously.
func (c *Config) QueueEnabled() bool { return c.Queue.Enabled && c.Redis.Address != "" }

// SignatureVerificationEnabled reports whether inbound payloads must carry a
// valid HMAC signature.
func (c *Config) SignatureVerificationEnabled() bool { return c.Payments.WebhookSecret != "" }

// IsProduction reports whether the service is running in a production-like env.
func (c *Config) IsProduction() bool { return c.App.Env == "production" }

// Validate rejects configurations that would fail confusingly at runtime.
func (c *Config) Validate() error {
	switch {
	case c.HTTP.Port < 0 || c.HTTP.Port > 65535:
		return fmt.Errorf("HTTP_PORT must be between 0 and 65535, got %d", c.HTTP.Port)
	case c.Database.MaxOpenConns <= 0:
		return fmt.Errorf("DATABASE_MAX_OPEN_CONNS must be positive")
	case c.HTTP.MaxInflightPayments <= 0:
		return fmt.Errorf("HTTP_MAX_INFLIGHT_PAYMENTS must be positive")
	case c.Payments.DefaultTermWeeks <= 0:
		return fmt.Errorf("PAYMENT_DEFAULT_TERM_WEEKS must be positive")
	case c.QueueEnabled() && c.Queue.Workers <= 0:
		return fmt.Errorf("QUEUE_WORKERS must be positive")
	case c.QueueEnabled() && c.Queue.BatchSize <= 0:
		return fmt.Errorf("QUEUE_BATCH_SIZE must be positive")
	case c.IsProduction() && !c.SignatureVerificationEnabled():
		return fmt.Errorf("PAYMENT_WEBHOOK_SECRET must be set in production")
	}
	return nil
}

// NewPaymentSettings and NewDeploymentDefaults hand the domain what it needs
// from the environment 
func NewPaymentSettings(c *Config) payment.Settings {
	return payment.Settings{
		ProviderLocation: c.Payments.providerLocation,
		DefaultCurrency:  c.Payments.Currency,
	}
}

func NewDeploymentDefaults(c *Config) deployment.Defaults {
	return deployment.Defaults{
		Currency:  c.Payments.Currency,
		Principal: c.Payments.DefaultPrincipal,
		TermWeeks: c.Payments.DefaultTermWeeks,
	}
}

// Module wires configuration into the FX graph.
var Module = fx.Module("config",
	fx.Provide(New, NewPaymentSettings, NewDeploymentDefaults),
)
