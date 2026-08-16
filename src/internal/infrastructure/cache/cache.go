package cache

import (
	"context"
	"encoding/json"
	"time"

	"asset-repayment-service/internal/config"
	"asset-repayment-service/internal/domain/deployment"
	"asset-repayment-service/internal/domain/payment"
	"github.com/redis/go-redis/v9"
	"go.uber.org/fx"
	"go.uber.org/zap"
)

type Cache struct {
	client    *redis.Client
	timeout   time.Duration
	refTTL    time.Duration
	resultTTL time.Duration
	logger    *zap.Logger
}

const (
	refKeyPrefix    = "arp:ref:"
	resultKeyPrefix = "arp:result:"
)

// New builds the cache. 
func New(cfg *config.Config, logger *zap.Logger, lc fx.Lifecycle) *Cache {
	log := logger.With(zap.String("module", "cache"))

	cache := &Cache{
		timeout:   cfg.Redis.Timeout,
		refTTL:    cfg.Redis.RefTTL,
		resultTTL: cfg.Redis.ResultTTL,
		logger:    log,
	}

	if !cfg.RedisEnabled() {
		log.Info("redis not configured, running without cache")
		return cache
	}

	cache.client = redis.NewClient(&redis.Options{
		Addr:         cfg.Redis.Address,
		Password:     cfg.Redis.Password,
		DB:           cfg.Redis.DB,
		PoolSize:     cfg.Redis.PoolSize,
		DialTimeout:  2 * time.Second,
		ReadTimeout:  cfg.Redis.Timeout,
		WriteTimeout: cfg.Redis.Timeout,
	})

	lc.Append(fx.Hook{
		OnStart: func(ctx context.Context) error {
			pingCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
			defer cancel()

			if err := cache.client.Ping(pingCtx).Err(); err != nil {
				// Non-fatal: refusing to start would turn a degraded
				// dependency into an outage.
				log.Warn("redis unreachable at startup, continuing without cache", zap.Error(err))
				cache.client = nil
				return nil
			}
			log.Info("cache connected", zap.String("address", cfg.Redis.Address))
			return nil
		},
		OnStop: func(context.Context) error {
			if cache.client == nil {
				return nil
			}
			return cache.client.Close()
		},
	})

	return cache
}

// GetRef returns the cached routing reference for a customer.
func (c *Cache) GetRef(ctx context.Context, customerID string) (*deployment.Ref, bool) {
	var ref deployment.Ref
	if !c.get(ctx, refKeyPrefix+customerID, &ref) {
		return nil, false
	}
	return &ref, true
}

// SetRef caches the routing reference. Only immutable fields are stored, so a
// stale entry cannot misreport a balance.
func (c *Cache) SetRef(ctx context.Context, ref deployment.Ref) {
	c.set(ctx, refKeyPrefix+ref.CustomerID, ref, c.refTTL)
}

// InvalidateRef drops a cached routing reference.
func (c *Cache) InvalidateRef(ctx context.Context, customerID string) {
	if c.client == nil {
		return
	}
	opCtx, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()

	if err := c.client.Del(opCtx, refKeyPrefix+customerID).Err(); err != nil {
		c.logger.Debug("cache invalidate failed", zap.String("customer_id", customerID), zap.Error(err))
	}
}

// GetResult returns the stored outcome for an already-processed reference.
func (c *Cache) GetResult(ctx context.Context, reference string) (*payment.Result, bool) {
	var result payment.Result
	if !c.get(ctx, resultKeyPrefix+reference, &result) {
		return nil, false
	}
	return &result, true
}

// SetResult caches a settled outcome, so retries skip the database.
func (c *Cache) SetResult(ctx context.Context, reference string, result *payment.Result) {
	if result == nil {
		return
	}

	trimmed := *result
	if trimmed.Payment != nil {
		payment := *trimmed.Payment
		payment.RawPayload = nil
		trimmed.Payment = &payment
	}

	c.set(ctx, resultKeyPrefix+reference, trimmed, c.resultTTL)
}

func (c *Cache) get(ctx context.Context, key string, target any) bool {
	if c.client == nil {
		return false
	}

	opCtx, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()

	raw, err := c.client.Get(opCtx, key).Bytes()
	if err != nil {
		if err != redis.Nil {
			c.logger.Debug("cache read failed", zap.String("key", key), zap.Error(err))
		}
		return false
	}
	if err := json.Unmarshal(raw, target); err != nil {
		c.logger.Warn("discarding unreadable cache entry", zap.String("key", key), zap.Error(err))
		return false
	}
	return true
}

func (c *Cache) set(ctx context.Context, key string, value any, ttl time.Duration) {
	if c.client == nil {
		return
	}

	encoded, err := json.Marshal(value)
	if err != nil {
		c.logger.Warn("cache encode failed", zap.String("key", key), zap.Error(err))
		return
	}

	opCtx, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()

	if err := c.client.Set(opCtx, key, encoded, ttl).Err(); err != nil {
		c.logger.Debug("cache write failed", zap.String("key", key), zap.Error(err))
	}
}

// Healthy reports reachability.
func (c *Cache) Healthy(ctx context.Context) bool {
	if c.client == nil {
		return true
	}
	opCtx, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()
	return c.client.Ping(opCtx).Err() == nil
}

// Module provides the cache under both interfaces it satisfies.
var Module = fx.Module("cache",
	fx.Provide(New),
	fx.Provide(func(c *Cache) deployment.RefCache { return c }),
	fx.Provide(func(c *Cache) payment.ResultCache { return c }),
)
