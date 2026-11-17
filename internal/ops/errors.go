package ops

import (
	"errors"
	"fmt"
	"net/http"
)

// Code is a typed error code shared by HTML, REST, the CLI and MCP.
type Code string

// The error codes. Responses are {"error": {"code": ..., "message": ...}}.
const (
	CodeUnauthenticated Code = "unauthenticated"
	CodeNotFound        Code = "not_found"
	CodeForbidden       Code = "forbidden"
	CodeInvalid         Code = "invalid"
	CodeConflict        Code = "conflict"
	CodeRateLimited     Code = "rate_limited"
	CodeProvider        Code = "provider_error"
	CodeInternal        Code = "internal"
	CodeTooLarge        Code = "too_large"
	CodeMethod          Code = "method_not_allowed"
	// CodeDryRunUnsupported: the op has effects a preview cannot undo.
	CodeDryRunUnsupported Code = "dry_run_unsupported"
	// CodeIdempotencyReuse: an Idempotency-Key was reused for a different request.
	CodeIdempotencyReuse Code = "idempotency_key_reuse"
	// CodeQuota: a write would take the owner, the person or the instance over
	// a storage quota.
	CodeQuota Code = "quota_exceeded"
	// CodeUnsupportedType: an upload is not a kind of file the server keeps.
	CodeUnsupportedType Code = "unsupported_type"
)

// Error is the one error type ops return.
type Error struct {
	Code    Code   `json:"code"`
	Message string `json:"message"`
	Field   string `json:"field,omitempty"`
	// Data carries structured detail for the client, e.g. the current revision
	// of a document on a conflict. Never put manuscript text here.
	Data map[string]any `json:"data,omitempty"`
	// RetryAfterSeconds is set for rate_limited.
	RetryAfterSeconds int `json:"retry_after,omitempty"`

	cause error // never serialised; logged by whoever turns the error into a response
}

// Cause returns the underlying error of an internal failure, for logging.
func (e *Error) Cause() error { return e.cause }

func (e *Error) Error() string { return string(e.Code) + ": " + e.Message }

// Constructors.
func Unauthenticated() *Error {
	return &Error{Code: CodeUnauthenticated, Message: "sign in required"}
}
func NotFound(what string) *Error {
	return &Error{Code: CodeNotFound, Message: what + " not found"}
}
func Forbidden(msg string) *Error { return &Error{Code: CodeForbidden, Message: msg} }
func Invalid(field, msg string) *Error {
	return &Error{Code: CodeInvalid, Field: field, Message: msg}
}
func Conflict(msg string) *Error { return &Error{Code: CodeConflict, Message: msg} }

// ConflictWith is a conflict that tells the client what the current state is.
func ConflictWith(msg string, data map[string]any) *Error {
	return &Error{Code: CodeConflict, Message: msg, Data: data}
}
func RateLimited(retryAfter int) *Error {
	return &Error{Code: CodeRateLimited, Message: "too many requests", RetryAfterSeconds: retryAfter}
}
func Internal(err error) *Error {
	return &Error{Code: CodeInternal, Message: "something went wrong on our side", cause: err}
}

// Reasons carried in data.reason of a trash refusal, so a client can tell it from a stale save.
const (
	ReasonProjectTrashed    = "project_trashed"
	ReasonCompendiumTrashed = "compendium_trashed"
)

// ProjectInTrash is the one answer to a write into a project that is in the trash (409, data.reason
// "project_trashed"). The registry gives it before any op runs; code that writes outside a request (an import
// that was already running) uses it to give the same answer.
func ProjectInTrash() *Error {
	return ConflictWith("This project is in the trash. Restore it to change anything in it.", map[string]any{"reason": ReasonProjectTrashed})
}

// CompendiumInTrash is the same for a Compendium (data.reason "compendium_trashed").
func CompendiumInTrash() *Error {
	return ConflictWith("This Compendium is in the trash. Restore it to change anything in it.", map[string]any{"reason": ReasonCompendiumTrashed})
}

// DryRunUnsupported is the refusal for an op that cannot be previewed.
func DryRunUnsupported(op string) *Error {
	return &Error{Code: CodeDryRunUnsupported, Message: op + " cannot be previewed with a dry run; it has effects a rollback cannot undo."}
}

// IdempotencyReuse is the refusal for a key reused with a different request.
func IdempotencyReuse() *Error {
	return &Error{Code: CodeIdempotencyReuse, Message: "That Idempotency-Key was already used for a different request. Use a new key for a new request."}
}

// TooLarge is a request body over the limit.
func TooLarge() *Error {
	return &Error{Code: CodeTooLarge, Message: "The request body is too large."}
}

// AsError converts any error into an *Error; unknown errors become internal.
func AsError(err error) *Error {
	if err == nil {
		return nil
	}
	var e *Error
	if errors.As(err, &e) {
		return e
	}
	return Internal(err)
}

// HTTPStatus maps a code to an HTTP status.
func (c Code) HTTPStatus() int {
	switch c {
	case CodeUnauthenticated:
		return http.StatusUnauthorized
	case CodeNotFound:
		return http.StatusNotFound
	case CodeForbidden:
		return http.StatusForbidden
	case CodeInvalid, CodeDryRunUnsupported, CodeIdempotencyReuse:
		return http.StatusUnprocessableEntity
	case CodeConflict:
		return http.StatusConflict
	case CodeRateLimited:
		return http.StatusTooManyRequests
	case CodeTooLarge, CodeQuota:
		return http.StatusRequestEntityTooLarge
	case CodeUnsupportedType:
		return http.StatusUnsupportedMediaType
	case CodeMethod:
		return http.StatusMethodNotAllowed
	case CodeProvider:
		return http.StatusBadGateway
	}
	return http.StatusInternalServerError
}

// Errorf is a small helper for Invalid with formatting.
func Invalidf(field, format string, a ...any) *Error {
	return Invalid(field, fmt.Sprintf(format, a...))
}
