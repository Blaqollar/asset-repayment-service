package payment

import (
	"errors"
	"testing"

	pkgerrors "asset-repayment-service/pkg/errors"
	"github.com/stretchr/testify/require"
)

// errDetails extracts the per-field validation detail from a domain error,
// failing the test if the error is not a validation failure.
func errDetails(t *testing.T, err error) map[string]any {
	t.Helper()

	var domainErr *pkgerrors.DomainError
	require.True(t, errors.As(err, &domainErr), "expected a DomainError, got %v", err)
	require.Equal(t, pkgerrors.CodeBadRequest, domainErr.Code)
	require.NotEmpty(t, domainErr.Details, "validation errors must carry field detail")

	return domainErr.Details
}
