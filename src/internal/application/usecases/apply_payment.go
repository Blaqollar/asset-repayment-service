package usecases

import (
	"context"
	"encoding/json"
	"time"

	"asset-repayment-service/internal/application/dtos"
	"asset-repayment-service/internal/domain/payment"
	"asset-repayment-service/internal/infrastructure/observability"
	"go.uber.org/zap"
)

// ApplyPaymentUsecase accepts one inbound payment notification.
type ApplyPaymentUsecase struct {
	payments payment.Service
	metrics  *observability.Metrics
	logger   *zap.Logger
}

// NewApplyPaymentUsecase creates the payment application usecase.
func NewApplyPaymentUsecase(
	payments payment.Service,
	metrics *observability.Metrics,
	logger *zap.Logger,
) *ApplyPaymentUsecase {
	return &ApplyPaymentUsecase{
		payments: payments,
		metrics:  metrics,
		logger:   logger.With(zap.String("usecase", "apply_payment")),
	}
}

// Execute reports what became of the notification: applied now, or queued.
func (uc *ApplyPaymentUsecase) Execute(
	ctx context.Context,
	req *dtos.ApplyPaymentRequest,
	raw json.RawMessage,
) (*dtos.ApplyPaymentResponse, error) {
	if req == nil {
		return nil, payment.ErrInvalidNotification
	}

	uc.metrics.PaymentsInflight.Inc()
	defer uc.metrics.PaymentsInflight.Dec()

	started := time.Now()
	result, err := uc.payments.Accept(ctx, req.ToNotification(), raw)
	elapsed := time.Since(started)

	if err != nil {
		uc.metrics.RecordPayment("error", elapsed)
		// Warn, not error: a malformed payload is the provider's defect.
		uc.logger.Warn("payment not applied",
			zap.String("customer_id", req.CustomerID),
			zap.String("transaction_reference", req.TransactionReference),
			zap.Error(err),
		)
		return nil, err
	}

	uc.metrics.RecordPayment(result.Outcome, elapsed)
	if result.Applied() && result.Payment != nil {
		// Inline path only; the worker counts what it applies.
		uc.metrics.AmountApplied.Add(result.Payment.Applied.InexactFloat64())
	}

	uc.logger.Debug("payment accepted",
		zap.String("customer_id", req.CustomerID),
		zap.String("transaction_reference", req.TransactionReference),
		zap.String("outcome", result.Outcome),
		zap.Duration("elapsed", elapsed),
	)

	return &dtos.ApplyPaymentResponse{
		Outcome:   result.Outcome,
		Duplicate: result.Outcome == payment.OutcomeDuplicate,
		Payment:   dtos.NewPaymentDTO(result.Payment),
		Position:  dtos.NewPositionDTO(result.Position),
	}, nil
}
