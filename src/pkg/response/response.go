package response

import (
	"encoding/json"
	"net/http"

	pkgerrors "asset-repayment-service/pkg/errors"
	"go.uber.org/zap"
)

// Envelope is the single response shape for every endpoint.
type Envelope struct {
	Status    string         `json:"status"`
	Message   string         `json:"message,omitempty"`
	Data      any            `json:"data,omitempty"`
	Errors    map[string]any `json:"errors,omitempty"`
	RequestID string         `json:"request_id,omitempty"`
}

const (
	statusSuccess = "success"
	statusError   = "error"
	RequestIDHeader = "X-Request-ID"
)

// Success writes a successful envelope.
func Success(w http.ResponseWriter, r *http.Request, code int, message string, data any) {
	write(w, r, code, Envelope{Status: statusSuccess, Message: message, Data: data})
}

// Error renders a DomainError with its own status code, and anything else as a
// generic 500
func Error(w http.ResponseWriter, r *http.Request, err error, logger *zap.Logger) {
	domainErr := pkgerrors.AsDomainError(err)

	if domainErr.Code >= pkgerrors.CodeInternal && logger != nil {
		logger.Error("request failed",
			zap.String("path", r.URL.Path),
			zap.String("request_id", w.Header().Get(RequestIDHeader)),
			zap.Error(err),
		)
	}

	write(w, r, domainErr.HTTPCode(), Envelope{
		Status:  statusError,
		Message: domainErr.Message,
		Errors:  domainErr.Details,
	})
}

func write(w http.ResponseWriter, _ *http.Request, code int, body Envelope) {
	body.RequestID = w.Header().Get(RequestIDHeader)

	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(code)

	_ = json.NewEncoder(w).Encode(body)
}
