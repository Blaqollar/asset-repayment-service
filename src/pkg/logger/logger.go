package logger

import (
	"os"
	"strings"

	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
)

// GetLogger builds the process-wide structured logger.
func GetLogger() (*zap.Logger, error) {
	var cfg zap.Config

	if strings.EqualFold(os.Getenv("APP_ENV"), "development") {
		cfg = zap.NewDevelopmentConfig()
		cfg.EncoderConfig.EncodeLevel = zapcore.CapitalColorLevelEncoder
		// zap's development preset attaches a stack trace to every warning.
		cfg.Development = false
	} else {
		cfg = zap.NewProductionConfig()
		cfg.Sampling = &zap.SamplingConfig{Initial: 100, Thereafter: 1000}
	}

	if lvl := os.Getenv("LOG_LEVEL"); lvl != "" {
		if parsed, err := zapcore.ParseLevel(lvl); err == nil {
			cfg.Level = zap.NewAtomicLevelAt(parsed)
		}
	}

	cfg.EncoderConfig.TimeKey = "timestamp"
	cfg.EncoderConfig.EncodeTime = zapcore.ISO8601TimeEncoder

	return cfg.Build()
}
