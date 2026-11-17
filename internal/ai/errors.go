package ai

import (
	"errors"
	"fmt"
	"net/url"
	"strings"
)

// Failure classes. Each has a plain-language message that is safe to show to
// any caller; the underlying detail goes to the server log only.
const (
	ClassBlocked     = "blocked"     // the address is not allowed (guard)
	ClassUnreachable = "unreachable" // could not connect, DNS, TLS
	ClassTimeout     = "timeout"
	ClassAuth        = "auth"        // 401, 403
	ClassCredit      = "credit"      // 402
	ClassRate        = "rate"        // 429
	ClassUnavailable = "unavailable" // 5xx
	ClassNotFound    = "not_found"   // 404: the model or the route
	ClassRejected    = "rejected"    // other 4xx: the provider refused the request
	ClassBadResponse = "bad_response"
	ClassTooLarge    = "too_large" // response over the cap
	ClassCancelled   = "cancelled"
)

// ProviderError is a failed provider call. Error and Message never contain an
// address, a key or a response body; Detail does not either, because it is
// scrubbed, but it is for the log only.
type ProviderError struct {
	Class      string
	Status     int
	Detail     string
	RetryAfter int // seconds, from Retry-After, when the provider gave one
}

func (e *ProviderError) Error() string { return "provider: " + e.Class }

// Message is the plain-language sentence for a caller.
func (e *ProviderError) Message() string {
	switch e.Class {
	case ClassBlocked:
		return "This provider's address is not one Scríobh may connect to. An administrator can mark a provider on this machine or the local network as local."
	case ClassUnreachable:
		return "The provider could not be reached. Check the address and that the service is running."
	case ClassTimeout:
		return "The provider took too long to answer."
	case ClassAuth:
		return "The provider rejected the key. Replace the key and try again."
	case ClassCredit:
		return "The provider says the account is out of credit."
	case ClassRate:
		return "The provider is busy or rate limiting this key. Wait a moment before trying again."
	case ClassUnavailable:
		return "The provider is having trouble (a server error on its side)."
	case ClassNotFound:
		return "The provider does not know that model or route. Check the model name and the base address."
	case ClassRejected:
		return "The provider refused the request."
	case ClassBadResponse:
		return "The provider answered in a way Scríobh could not understand."
	case ClassTooLarge:
		return "The provider's answer was larger than this server accepts and was cut off."
	case ClassCancelled:
		return "The request was cancelled."
	}
	return "The provider call failed."
}

// Retryable reports whether trying the same request again, or the next
// provider in a chain, can help. Authentication, policy and request errors do
// not: a fallback must not be used to get around a key or a rule.
func (e *ProviderError) Retryable() bool {
	switch e.Class {
	case ClassTimeout, ClassRate, ClassUnavailable, ClassUnreachable:
		return true
	}
	return false
}

// AsProviderError returns err as a *ProviderError, if it is one.
func AsProviderError(err error) (*ProviderError, bool) {
	var pe *ProviderError
	if errors.As(err, &pe) {
		return pe, true
	}
	return nil, false
}

func perr(class string, status int, format string, a ...any) *ProviderError {
	return &ProviderError{Class: class, Status: status, Detail: fmt.Sprintf(format, a...)}
}

// classFromStatus maps an HTTP status to a class.
func classFromStatus(status int) string {
	switch {
	case status == 401 || status == 403:
		return ClassAuth
	case status == 402:
		return ClassCredit
	case status == 404:
		return ClassNotFound
	case status == 408:
		return ClassTimeout
	case status == 429:
		return ClassRate
	case status >= 500:
		return ClassUnavailable
	case status >= 400:
		return ClassRejected
	}
	return ClassBadResponse
}

// ScrubError removes anything address- or credential-shaped from an error text
// from the standard library (a transport error repeats the URL, which can hold
// user information) and from the secrets the caller names.
func ScrubError(err error, secret ...string) string {
	if err == nil {
		return ""
	}
	s := err.Error()
	var ue *url.Error
	if errors.As(err, &ue) {
		s = ue.Op + ": " + ue.Err.Error()
	}
	for _, v := range secret {
		if len(v) >= 4 {
			s = strings.ReplaceAll(s, v, "[redacted]")
		}
	}
	return s
}
