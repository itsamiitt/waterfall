package domain

import "errors"

// ErrorClass is the normalized provider-error taxonomy from
// skills/api-integration/SKILL.md. Every provider adapter maps its HTTP/status quirks
// (e.g. 402 -> Quota, Hunter 403 -> RateLimit) onto exactly one of these classes so the
// Execution Engine can make retry/breaker/failover decisions uniformly.
type ErrorClass int

const (
	ClassUnknown      ErrorClass = iota
	ClassAuth                    // bad/expired credential — do NOT retry, alert (401/most 403)
	ClassRateLimit               // throttled — retry with backoff, feeds the breaker (429, Hunter 403)
	ClassTransient               // transient network/5xx — retry with capped jittered backoff
	ClassNotFound                // no data for this subject — terminal success-with-no-value, no retry
	ClassBadRequest              // malformed request — do NOT retry, it will always fail (400/422)
	ClassQuota                   // credits/plan exhausted — do NOT retry this key, failover pool (402)
	ClassProviderDown            // provider outage — open the breaker, failover
)

func (c ErrorClass) String() string {
	switch c {
	case ClassAuth:
		return "AUTH"
	case ClassRateLimit:
		return "RATE_LIMIT"
	case ClassTransient:
		return "TRANSIENT"
	case ClassNotFound:
		return "NOT_FOUND"
	case ClassBadRequest:
		return "BAD_REQUEST"
	case ClassQuota:
		return "QUOTA"
	case ClassProviderDown:
		return "PROVIDER_DOWN"
	default:
		return "UNKNOWN"
	}
}

// ProviderError is a classified provider failure. Adapters return it so the engine
// never has to parse raw HTTP status codes.
type ProviderError struct {
	Provider string
	Class    ErrorClass
	Err      error
}

func (e *ProviderError) Error() string {
	return e.Provider + ": " + e.Class.String() + ": " + e.Err.Error()
}

func (e *ProviderError) Unwrap() error { return e.Err }

// NewProviderError builds a classified error for a provider.
func NewProviderError(provider string, class ErrorClass, err error) *ProviderError {
	return &ProviderError{Provider: provider, Class: class, Err: err}
}

// ClassOf extracts the ErrorClass from any error in the chain, defaulting to
// ClassTransient for unclassified errors (fail safe: an unknown error is retryable but
// bounded, never silently treated as success).
func ClassOf(err error) ErrorClass {
	if err == nil {
		return ClassUnknown
	}
	var pe *ProviderError
	if errors.As(err, &pe) {
		return pe.Class
	}
	return ClassTransient
}

// Retryable reports whether an error of this class should be retried at all (G3
// bounds how many times; this decides whether to try again in principle).
func (c ErrorClass) Retryable() bool {
	switch c {
	case ClassRateLimit, ClassTransient, ClassProviderDown:
		return true
	default: // Auth, NotFound, BadRequest, Quota, Unknown -> terminal for this key
		return false
	}
}
