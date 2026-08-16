package middleware

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"asset-repayment-service/internal/config"
	"asset-repayment-service/internal/domain/payment"
	"asset-repayment-service/internal/infrastructure/observability"
	pkgerrors "asset-repayment-service/pkg/errors"
	"asset-repayment-service/pkg/response"
	"github.com/google/uuid"
	"go.uber.org/zap"
)

type contextKey string

const (
	requestIDKey contextKey = "request_id"
	loggerKey    contextKey = "logger"
	rawBodyKey   contextKey = "raw_body"
)

// Middleware is a composable HTTP decorator.
type Middleware func(http.Handler) http.Handler

// Chain applies middleware so the first listed runs outermost.
func Chain(h http.Handler, middlewares ...Middleware) http.Handler {
	for i := len(middlewares) - 1; i >= 0; i-- {
		h = middlewares[i](h)
	}
	return h
}

// RequestIDFrom returns the correlation id for a request.
func RequestIDFrom(ctx context.Context) string {
	if id, ok := ctx.Value(requestIDKey).(string); ok {
		return id
	}
	return ""
}

// RawBodyFrom returns the verbatim request body captured by Body.
func RawBodyFrom(ctx context.Context) []byte {
	if body, ok := ctx.Value(rawBodyKey).([]byte); ok {
		return body
	}
	return nil
}

// RequestID propagates the provider's correlation id, or mints one.
func RequestID(logger *zap.Logger) Middleware {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			id := r.Header.Get(response.RequestIDHeader)
			if id == "" {
				id = uuid.NewString()
			}

			ctx := context.WithValue(r.Context(), requestIDKey, id)
			ctx = context.WithValue(ctx, loggerKey, logger.With(zap.String("request_id", id)))

			w.Header().Set(response.RequestIDHeader, id)
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}

// Recovery turns a panic into a 500 rather than killing every other in-flight
// payment on the pod.
func Recovery(logger *zap.Logger) Middleware {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			defer func() {
				if recovered := recover(); recovered != nil {
					logger.Error("panic recovered",
						zap.Any("panic", recovered),
						zap.String("path", r.URL.Path),
						zap.String("request_id", RequestIDFrom(r.Context())),
						zap.Stack("stack"),
					)
					response.Error(w, r, pkgerrors.Internal(), nil)
				}
			}()
			next.ServeHTTP(w, r)
		})
	}
}

// statusRecorder captures the status code without buffering the body.
type statusRecorder struct {
	http.ResponseWriter
	status int
}

func (s *statusRecorder) WriteHeader(code int) {
	s.status = code
	s.ResponseWriter.WriteHeader(code)
}

func (s *statusRecorder) Write(b []byte) (int, error) {
	if s.status == 0 {
		s.status = http.StatusOK
	}
	return s.ResponseWriter.Write(b)
}

// Observability records latency and outcome per route.
func Observability(route string, metrics *observability.Metrics) Middleware {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			started := time.Now()
			recorder := &statusRecorder{ResponseWriter: w}

			next.ServeHTTP(recorder, r)

			if recorder.status == 0 {
				recorder.status = http.StatusOK
			}
			metrics.RecordHTTP(route, r.Method, statusClass(recorder.status), time.Since(started))
		})
	}
}

func statusClass(status int) string {
	return strconv.Itoa(status/100) + "xx"
}

// Body reads and bounds the request body once, since a request body is a
// single-use stream and both the signature check and the handler need it.
func Body(maxBytes int64) Middleware {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			limited := http.MaxBytesReader(w, r.Body, maxBytes)

			body, err := io.ReadAll(limited)
			if err != nil {
				response.Error(w, r, pkgerrors.BadRequest(
					fmt.Sprintf("request body exceeds the %d byte limit or could not be read", maxBytes),
				), nil)
				return
			}

			ctx := context.WithValue(r.Context(), rawBodyKey, body)
			r = r.WithContext(ctx)
			r.Body = io.NopCloser(bytes.NewReader(body))

			next.ServeHTTP(w, r)
		})
	}
}

// Timeout bounds the work independently of the client: a provider that hangs
// up must not leave a statement running.
func Timeout(timeout time.Duration) Middleware {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			ctx, cancel := context.WithTimeout(r.Context(), timeout)
			defer cancel()

			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}

// LoadShedding caps concurrent work with a counting semaphore. 
func LoadShedding(ceiling int, metrics *observability.Metrics) Middleware {
	admission := make(chan struct{}, ceiling)

	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			select {
			case admission <- struct{}{}:
				defer func() { <-admission }()
			default:
				metrics.PaymentsShed.Inc()
				metrics.PaymentsTotal.WithLabelValues("shed").Inc()
				response.Error(w, r, payment.ErrOverloaded, nil)
				return
			}

			next.ServeHTTP(w, r)
		})
	}
}

// Signature verifies the provider's HMAC-SHA256 over the raw body.
func Signature(cfg *config.Config, logger *zap.Logger) Middleware {
	return func(next http.Handler) http.Handler {
		if !cfg.SignatureVerificationEnabled() {
			logger.Warn("payment signature verification is disabled; enable it before production use")
			return next
		}

		secret := []byte(cfg.Payments.WebhookSecret)
		header := cfg.Payments.SignatureHeader

		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			provided := strings.TrimSpace(r.Header.Get(header))
			if provided == "" {
				response.Error(w, r, pkgerrors.Unauthorized(), nil)
				return
			}
			provided = strings.TrimPrefix(provided, "sha256=")

			mac := hmac.New(sha256.New, secret)
			mac.Write(RawBodyFrom(r.Context()))
			expected := hex.EncodeToString(mac.Sum(nil))

			if subtle.ConstantTimeCompare([]byte(strings.ToLower(provided)), []byte(expected)) != 1 {
				logger.Warn("rejected payment notification with invalid signature",
					zap.String("request_id", RequestIDFrom(r.Context())),
					zap.String("remote_addr", r.RemoteAddr),
				)
				response.Error(w, r, pkgerrors.Unauthorized(), nil)
				return
			}

			next.ServeHTTP(w, r)
		})
	}
}
