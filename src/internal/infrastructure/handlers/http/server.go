package httpx

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"time"

	"asset-repayment-service/internal/config"
	"go.uber.org/fx"
	"go.uber.org/zap"
)

// Server owns the HTTP listener lifecycle.
type Server struct {
	server *http.Server
	port   int
}

// NewServer builds the HTTP server and binds it to the FX lifecycle.
func NewServer(cfg *config.Config, handler *http.ServeMux, log *zap.Logger, lc fx.Lifecycle) (*Server, error) {
	logger := log.With(zap.String("module", "http"))
	port := cfg.HTTP.Port

	server := &http.Server{
		Addr:    fmt.Sprintf(":%d", port),
		Handler: handler,
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       cfg.HTTP.ReadTimeout,
		WriteTimeout:      cfg.HTTP.WriteTimeout,
		IdleTimeout:       cfg.HTTP.IdleTimeout,
		ErrorLog: zap.NewStdLog(logger),
	}

	instance := &Server{server: server, port: port}

	lc.Append(fx.Hook{
		OnStart: func(context.Context) error {

			listener, err := net.Listen("tcp", server.Addr)
			if err != nil {
				return fmt.Errorf("listen on %s: %w", server.Addr, err)
			}
			instance.port = listener.Addr().(*net.TCPAddr).Port

			go func() {
				logger.Info("http server listening", zap.Int("port", instance.port))
				if err := server.Serve(listener); err != nil && !errors.Is(err, http.ErrServerClosed) {
					logger.Error("http server stopped unexpectedly", zap.Error(err))
				}
			}()
			return nil
		},
		OnStop: func(ctx context.Context) error {
			shutdownCtx, cancel := context.WithTimeout(ctx, cfg.HTTP.ShutdownTimeout)
			defer cancel()

			logger.Info("http server shutting down")
			return server.Shutdown(shutdownCtx)
		},
	})

	return instance, nil
}

// Port returns the bound port, which differs from the configured one when 0.
func (s *Server) Port() int { return s.port }
