package client

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/sbalaji6/agent-platform-contracts/platform"
)

// Codes the client assigns itself. They are prefixed so they never collide with platform codes.
const (
	// CodeTransport means the request could not be sent or its response could not be read,
	// including the client's own per-request timeout. Retryable: CreateExecution is idempotent by
	// key, and the other calls are safe to repeat.
	CodeTransport platform.ErrorCode = "CLIENT_TRANSPORT"
	// CodeUnexpectedResponse means the platform answered with a body the client could not decode,
	// such as an HTML error page from a proxy. Error responses of this kind are retryable for 429 and
	// 5xx statuses.
	CodeUnexpectedResponse platform.ErrorCode = "CLIENT_UNEXPECTED_RESPONSE"
)

// Error is a request-level rejection, a failed execution (see ExecutionError), or a transport
// failure. Decide retries from Retryable, never from Code.
type Error struct {
	// StatusCode is the HTTP status, or 0 for transport failures and failed executions.
	StatusCode int
	Code       platform.ErrorCode
	Message    string
	Retryable  bool
	// RetryAfter is the minimum wait before retrying, from retry_after_ms or else the Retry-After
	// header. Zero if neither was given.
	RetryAfter time.Duration
	Details    []platform.FieldError
	// RateLimit is parsed from the response headers, or nil.
	RateLimit *RateLimit
	// ExecutionID is set when the error is the failure of an execution.
	ExecutionID string

	cause error
}

func (e *Error) Error() string {
	var b strings.Builder
	b.WriteString("agent platform: ")
	if e.StatusCode != 0 {
		fmt.Fprintf(&b, "HTTP %d: ", e.StatusCode)
	}
	if e.ExecutionID != "" {
		fmt.Fprintf(&b, "execution %s: ", e.ExecutionID)
	}
	fmt.Fprintf(&b, "%s: %s", e.Code, e.Message)
	return b.String()
}

// Unwrap returns the underlying transport or decoding error, if any.
func (e *Error) Unwrap() error { return e.cause }

// IsRetryable reports whether err is an *Error with Retryable set. Errors from the caller's own
// context ending are not retryable.
func IsRetryable(err error) bool {
	var e *Error
	return errors.As(err, &e) && e.Retryable
}

// RetryAfter returns the RetryAfter of an *Error in err's chain, or 0.
func RetryAfter(err error) time.Duration {
	var e *Error
	if errors.As(err, &e) {
		return e.RetryAfter
	}
	return 0
}

// ExecutionError returns the failure of a failed execution as an *Error, or nil if x did not fail.
// Cancelled executions are not failures; check x.Status for them.
func ExecutionError(x *platform.Execution) error {
	if x == nil || x.Status != platform.ExecutionFailed {
		return nil
	}
	if x.Error == nil {
		return &Error{Code: CodeUnexpectedResponse, Message: "execution failed without an error object", ExecutionID: x.ExecutionID}
	}
	e := fromPlatform(*x.Error)
	e.ExecutionID = x.ExecutionID
	return e
}

func fromPlatform(pe platform.Error) *Error {
	e := &Error{Code: pe.Code, Message: pe.Message, Retryable: pe.Retryable, Details: pe.Details}
	if pe.RetryAfterMs != nil {
		e.RetryAfter = time.Duration(*pe.RetryAfterMs) * time.Millisecond
	}
	return e
}

// decodeError builds the *Error for a non-2xx response. The envelope's retry_after_ms wins over the
// Retry-After header, as the spec says.
func decodeError(status int, h http.Header, body []byte, rl *RateLimit, now time.Time) *Error {
	var env platform.ErrorEnvelope
	var e *Error
	bodyRetryAfter := false
	if json.Unmarshal(body, &env) == nil && env.Error.Code != "" {
		e = fromPlatform(env.Error)
		bodyRetryAfter = env.Error.RetryAfterMs != nil
	} else {
		e = &Error{
			Code:      CodeUnexpectedResponse,
			Message:   fmt.Sprintf("HTTP %d without an error envelope: %s", status, snippet(body)),
			Retryable: status == http.StatusTooManyRequests || status >= 500,
		}
	}
	e.StatusCode = status
	e.RateLimit = rl
	if !bodyRetryAfter {
		if d, ok := parseRetryAfter(h.Get(platform.HeaderRetryAfter), now); ok {
			e.RetryAfter = d
		}
	}
	return e
}

// parseRetryAfter parses a Retry-After value: delay-seconds or an HTTP-date (RFC 9110 section
// 10.2.3). A date in the past yields 0.
func parseRetryAfter(v string, now time.Time) (time.Duration, bool) {
	v = strings.TrimSpace(v)
	if v == "" {
		return 0, false
	}
	if n, err := strconv.ParseInt(v, 10, 64); err == nil {
		if n < 0 {
			return 0, false
		}
		return time.Duration(n) * time.Second, true
	}
	t, err := http.ParseTime(v)
	if err != nil {
		return 0, false
	}
	return max(t.Sub(now), 0), true
}

func snippet(body []byte) string {
	s := strings.Join(strings.Fields(string(body)), " ")
	if len(s) > 200 {
		s = s[:200] + "..."
	}
	if s == "" {
		return "(empty body)"
	}
	return s
}
