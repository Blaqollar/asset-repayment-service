package errors

// ResponseCode represents HTTP response codes carried by a DomainError.
type ResponseCode int

const (
	// 2xx Success
	CodeOK       ResponseCode = 200
	CodeCreated  ResponseCode = 201
	CodeAccepted ResponseCode = 202

	// 4xx Client Errors
	CodeBadRequest         ResponseCode = 400
	CodeUnauthorized       ResponseCode = 401
	CodeForbidden          ResponseCode = 403
	CodeNotFound           ResponseCode = 404
	CodeConflict           ResponseCode = 409
	CodeFailedPrecondition ResponseCode = 412
	CodeTooManyRequests    ResponseCode = 429

	// 5xx Server Errors
	CodeInternal    ResponseCode = 500
	CodeUnavailable ResponseCode = 503
)
