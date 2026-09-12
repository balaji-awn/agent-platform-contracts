package platform

import (
	"net/http"
	"slices"
)

// ErrorCode is the machine-readable code of an Error. The set may grow, so handle unknown codes by
// Error.Retryable.
type ErrorCode string

const (
	CodeRequestInvalid        ErrorCode = "REQUEST_INVALID"
	CodeInputSchemaInvalid    ErrorCode = "INPUT_SCHEMA_INVALID"
	CodeOutputSchemaViolation ErrorCode = "OUTPUT_SCHEMA_VIOLATION"
	CodeAgentNotFound         ErrorCode = "AGENT_NOT_FOUND"
	CodeAgentVersionNotFound  ErrorCode = "AGENT_VERSION_NOT_FOUND"
	CodeAgentVersionDisabled  ErrorCode = "AGENT_VERSION_DISABLED"
	CodeExecutionNotFound     ErrorCode = "EXECUTION_NOT_FOUND"
	CodeIdempotencyKeyReused  ErrorCode = "IDEMPOTENCY_KEY_REUSED"
	CodeTimeout               ErrorCode = "TIMEOUT"
	CodeProviderRateLimited   ErrorCode = "PROVIDER_RATE_LIMITED"
	CodeProviderUnavailable   ErrorCode = "PROVIDER_UNAVAILABLE"
	CodeQuotaExceeded         ErrorCode = "QUOTA_EXCEEDED"
	CodeUnauthorized          ErrorCode = "UNAUTHORIZED"
	CodeForbidden             ErrorCode = "FORBIDDEN"
	CodeInternal              ErrorCode = "INTERNAL"
)

// CodeInfo is how a code is delivered, from the table in the Error schema of api/openapi.yaml.
type CodeInfo struct {
	// HTTPStatus is the status of a request-level rejection with this code, or 0 if the code only
	// appears in failed executions.
	HTTPStatus int
	// ExecutionLevel reports whether the code can appear in the error of a failed execution.
	ExecutionLevel bool
	// Retryable is the default of Error.Retryable. Servers set the flag per response, and callers
	// must decide from the flag.
	Retryable bool
}

var codeTable = map[ErrorCode]CodeInfo{
	CodeRequestInvalid:        {HTTPStatus: http.StatusBadRequest},
	CodeInputSchemaInvalid:    {HTTPStatus: http.StatusBadRequest},
	CodeUnauthorized:          {HTTPStatus: http.StatusUnauthorized},
	CodeForbidden:             {HTTPStatus: http.StatusForbidden},
	CodeAgentNotFound:         {HTTPStatus: http.StatusNotFound},
	CodeAgentVersionNotFound:  {HTTPStatus: http.StatusNotFound},
	CodeExecutionNotFound:     {HTTPStatus: http.StatusNotFound},
	CodeAgentVersionDisabled:  {HTTPStatus: http.StatusConflict},
	CodeIdempotencyKeyReused:  {HTTPStatus: http.StatusConflict},
	CodeQuotaExceeded:         {HTTPStatus: http.StatusTooManyRequests, Retryable: true},
	CodeProviderRateLimited:   {HTTPStatus: http.StatusTooManyRequests, ExecutionLevel: true, Retryable: true},
	CodeProviderUnavailable:   {HTTPStatus: http.StatusServiceUnavailable, ExecutionLevel: true, Retryable: true},
	CodeInternal:              {HTTPStatus: http.StatusInternalServerError, ExecutionLevel: true, Retryable: true},
	CodeTimeout:               {ExecutionLevel: true, Retryable: true},
	CodeOutputSchemaViolation: {ExecutionLevel: true},
}

// Info returns the spec's defaults for c. ok is false for codes this package does not know.
func (c ErrorCode) Info() (info CodeInfo, ok bool) {
	info, ok = codeTable[c]
	return info, ok
}

// Codes returns every code this package knows, sorted.
func Codes() []ErrorCode {
	codes := make([]ErrorCode, 0, len(codeTable))
	for c := range codeTable {
		codes = append(codes, c)
	}
	slices.Sort(codes)
	return codes
}

// Error is the error object of an ErrorEnvelope or of a failed Execution.
type Error struct {
	Code         ErrorCode    `json:"code"`
	Message      string       `json:"message"`
	Retryable    bool         `json:"retryable"`
	RetryAfterMs *int64       `json:"retry_after_ms,omitempty"`
	Details      []FieldError `json:"details,omitempty"`
}

// ErrorEnvelope is the body of every request-level rejection.
type ErrorEnvelope struct {
	Error Error `json:"error"`
}

// FieldError points at one problem in a request. Path is a JSON Pointer.
type FieldError struct {
	Path    string `json:"path"`
	Message string `json:"message"`
}
