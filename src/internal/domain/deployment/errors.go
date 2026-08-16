package deployment

import pkgerrors "asset-repayment-service/pkg/errors"

var (

	ErrNotFound = pkgerrors.NotFound("deployment")
	ErrNoOpenDeployment = pkgerrors.FailedPrecondition("customer has no open deployment")
	ErrDuplicateActiveDeployment = pkgerrors.Conflict("customer already has an open deployment")
	ErrInvalidCustomerID = pkgerrors.BadRequest("customer_id is required")
	ErrInvalidPrincipal  = pkgerrors.BadRequest("principal must be greater than zero")
	ErrInvalidTerm       = pkgerrors.BadRequest("term_weeks must be between 1 and 520")
)
