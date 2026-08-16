package httpx

import (
	"encoding/json"
	"net/http"

	"asset-repayment-service/internal/application/dtos"
	"asset-repayment-service/internal/domain/payment"
	pkgerrors "asset-repayment-service/pkg/errors"
	"asset-repayment-service/pkg/middleware"
	"asset-repayment-service/pkg/response"
)

// ApplyPayment is the endpoint the bank calls on every successful transfer.
func (h *Handlers) ApplyPayment(w http.ResponseWriter, r *http.Request) {
	raw := middleware.RawBodyFrom(r.Context())

	var req dtos.ApplyPaymentRequest
	if err := json.Unmarshal(raw, &req); err != nil {
		response.Error(w, r, pkgerrors.BadRequest("request body must be valid JSON"), nil)
		return
	}

	result, err := h.applyPayment.Execute(r.Context(), &req, raw)
	if err != nil {
		response.Error(w, r, err, h.logger)
		return
	}

	// 200 applied (or replayed); 202 accepted but the balance has not moved —
	// queued, unmatched, or unsettled. Both terminal: neither is retryable.
	status := http.StatusOK
	switch result.Outcome {
	case payment.OutcomeQueued, payment.OutcomeUnmatched, payment.OutcomeIgnored:
		status = http.StatusAccepted
	}

	response.Success(w, r, status, outcomeMessage(result.Outcome), result)
}

// GetPosition quotes the customer's standing on demand, for when nobody has
// just paid.
func (h *Handlers) GetPosition(w http.ResponseWriter, r *http.Request) {
	position, err := h.getPosition.Execute(r.Context(), r.PathValue("customer_id"))
	if err != nil {
		response.Error(w, r, err, h.logger)
		return
	}
	response.Success(w, r, http.StatusOK, "", position)
}

func outcomeMessage(outcome string) string {
	switch outcome {
	case payment.OutcomeApplied:
		return "payment applied to outstanding balance"
	case payment.OutcomeDuplicate:
		return "payment already processed; original result returned"
	case payment.OutcomeUnmatched:
		return "payment recorded but no open deployment matched this customer"
	case payment.OutcomeIgnored:
		return "payment recorded but not applied; provider status is not a settled credit"
	case payment.OutcomeQueued:
		return "payment accepted and queued; the position updates once a worker applies it"
	default:
		return "payment processed"
	}
}
