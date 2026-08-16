package httpx

import (
	"net/http"

	pkgerrors "asset-repayment-service/pkg/errors"
	"asset-repayment-service/pkg/response"
	"go.uber.org/zap"
)

// Healthz is liveness
func (h *Handlers) Healthz(w http.ResponseWriter, _ *http.Request) {
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte("OK"))
}

// Readyz is readiness, which requires the database. The cache is excluded
// because the service is correct without it.
func (h *Handlers) Readyz(w http.ResponseWriter, r *http.Request) {
	if err := h.db.PingContext(r.Context()); err != nil {
		h.logger.Warn("readiness check failed", zap.Error(err))
		response.Error(w, r, pkgerrors.Unavailable(), nil)
		return
	}

	cacheStatus := "ok"
	if !h.cache.Healthy(r.Context()) {
		cacheStatus = "degraded"
	}

	response.Success(w, r, http.StatusOK, "ready", map[string]any{
		"database": "ok",
		"cache":    cacheStatus,
	})
}
