package httpx

import (
	"encoding/json"
	"net/http"

	"asset-repayment-service/internal/application/dtos"
	pkgerrors "asset-repayment-service/pkg/errors"
	"asset-repayment-service/pkg/middleware"
	"asset-repayment-service/pkg/response"
)

// CreateDeployment registers an asset against a customer
func (h *Handlers) CreateDeployment(w http.ResponseWriter, r *http.Request) {
	var req dtos.CreateDeploymentRequest
	if err := json.Unmarshal(middleware.RawBodyFrom(r.Context()), &req); err != nil {
		response.Error(w, r, pkgerrors.BadRequest("request body must be valid JSON"), nil)
		return
	}

	created, err := h.createDeployment.Execute(r.Context(), &req)
	if err != nil {
		response.Error(w, r, err, h.logger)
		return
	}
	response.Success(w, r, http.StatusCreated, "deployment registered", created)
}
