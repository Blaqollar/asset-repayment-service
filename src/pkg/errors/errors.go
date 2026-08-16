package errors

import (
	"errors"
	"fmt"
)

// DomainError represents a domain-level error with a sanitized, user-facing
// message. 
type DomainError struct {
	Code     ResponseCode
	Message  string
	Details  map[string]any
	internal error
}

func (e *DomainError) Error() string {
	if e.internal != nil {
		return fmt.Sprintf("[%d] %s: %v", e.Code, e.Message, e.internal)
	}
	return fmt.Sprintf("[%d] %s", e.Code, e.Message)
}

func (e *DomainError) Unwrap() error { return e.internal }

// HTTPCode returns the HTTP status code for this error.
func (e *DomainError) HTTPCode() int { return int(e.Code) }

// WithCause attaches an internal cause without mutating the shared sentinel.
func (e *DomainError) WithCause(err error) *DomainError {
	if e == nil {
		return Internal().WithCause(err)
	}
	clone := *e
	clone.internal = err
	return &clone
}

// WithDetails attaches structured field-level detail (e.g. validation errors).
func (e *DomainError) WithDetails(details map[string]any) *DomainError {
	if e == nil {
		return Internal()
	}
	clone := *e
	clone.Details = details
	return &clone
}

// Constructors

func NotFound(resource string) *DomainError {
	return &DomainError{Code: CodeNotFound, Message: fmt.Sprintf("%s not found", resource)}
}

func BadRequest(reason string) *DomainError {
	return &DomainError{Code: CodeBadRequest, Message: reason}
}

func Unauthorized() *DomainError {
	return &DomainError{Code: CodeUnauthorized, Message: "unauthorized"}
}

func Forbidden(reason string) *DomainError {
	return &DomainError{Code: CodeForbidden, Message: reason}
}

func Conflict(reason string) *DomainError {
	return &DomainError{Code: CodeConflict, Message: reason}
}

func FailedPrecondition(reason string) *DomainError {
	return &DomainError{Code: CodeFailedPrecondition, Message: reason}
}

func TooManyRequests(reason string) *DomainError {
	return &DomainError{Code: CodeTooManyRequests, Message: reason}
}

func ValidationFailed(details map[string]any) *DomainError {
	return &DomainError{Code: CodeBadRequest, Message: "validation failed", Details: details}
}

func Internal() *DomainError {
	return &DomainError{Code: CodeInternal, Message: "internal server error"}
}

func Unavailable() *DomainError {
	return &DomainError{Code: CodeUnavailable, Message: "service unavailable"}
}

// AsDomainError unwraps err into a *DomainError, falling back to Internal()
// so that transport layers never leak a raw driver or driver-wrapped message.
func AsDomainError(err error) *DomainError {
	if err == nil {
		return nil
	}
	var de *DomainError
	if errors.As(err, &de) {
		return de
	}
	return Internal().WithCause(err)
}
