package client

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"testing"
	"time"

	"github.com/sbalaji6/agent-platform-contracts/platform"
)

var now = time.Date(2026, 9, 11, 12, 0, 0, 0, time.UTC)

func header(kv ...string) http.Header {
	h := http.Header{}
	for i := 0; i+1 < len(kv); i += 2 {
		h.Set(kv[i], kv[i+1])
	}
	return h
}

func TestDecodeError(t *testing.T) {
	envelope := func(code string, retryable bool, extra string) string {
		return fmt.Sprintf(`{"error":{"code":%q,"message":"m","retryable":%t%s}}`, code, retryable, extra)
	}
	tests := []struct {
		name       string
		status     int
		header     http.Header
		body       string
		code       platform.ErrorCode
		retryable  bool
		retryAfter time.Duration
		details    int
	}{
		{"non-retryable with details", 400, nil,
			envelope("INPUT_SCHEMA_INVALID", false, `,"details":[{"path":"/alert","message":"is required"}]`),
			platform.CodeInputSchemaInvalid, false, 0, 1},
		{"body retry_after_ms wins over header", 429, header("Retry-After", "9"),
			envelope("QUOTA_EXCEEDED", true, `,"retry_after_ms":1500`),
			platform.CodeQuotaExceeded, true, 1500 * time.Millisecond, 0},
		{"body retry_after_ms of zero still wins", 503, header("Retry-After", "9"),
			envelope("PROVIDER_UNAVAILABLE", true, `,"retry_after_ms":0`),
			platform.CodeProviderUnavailable, true, 0, 0},
		{"header seconds without body field", 503, header("Retry-After", "4"),
			envelope("PROVIDER_UNAVAILABLE", true, ""),
			platform.CodeProviderUnavailable, true, 4 * time.Second, 0},
		{"header HTTP-date", 503, header("Retry-After", now.Add(30*time.Second).Format(http.TimeFormat)),
			envelope("PROVIDER_UNAVAILABLE", true, ""),
			platform.CodeProviderUnavailable, true, 30 * time.Second, 0},
		{"flag wins over code", 503, nil,
			envelope("PROVIDER_UNAVAILABLE", false, ""),
			platform.CodeProviderUnavailable, false, 0, 0},
		{"unknown code keeps flag", 400, nil,
			envelope("SOMETHING_NEW", true, ""),
			"SOMETHING_NEW", true, 0, 0},
		{"conflict", 409, nil,
			envelope("IDEMPOTENCY_KEY_REUSED", false, ""),
			platform.CodeIdempotencyKeyReused, false, 0, 0},
		{"HTML 502 from a proxy", 502, nil, `<html><body>Bad Gateway</body></html>`,
			CodeUnexpectedResponse, true, 0, 0},
		{"empty 429 with header", 429, header("Retry-After", "2"), ``,
			CodeUnexpectedResponse, true, 2 * time.Second, 0},
		{"plain-text 404", 404, nil, `404 page not found`,
			CodeUnexpectedResponse, false, 0, 0},
		{"envelope without code", 500, nil, `{"error":{}}`,
			CodeUnexpectedResponse, true, 0, 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rl := &RateLimit{Limit: 10}
			e := decodeError(tt.status, tt.header, []byte(tt.body), rl, now)
			if e.StatusCode != tt.status || e.Code != tt.code || e.Retryable != tt.retryable ||
				e.RetryAfter != tt.retryAfter || len(e.Details) != tt.details || e.RateLimit != rl {
				t.Errorf("got %+v", e)
			}
			if IsRetryable(e) != tt.retryable {
				t.Errorf("IsRetryable = %t", IsRetryable(e))
			}
		})
	}
}

func TestParseRetryAfter(t *testing.T) {
	tests := []struct {
		in   string
		want time.Duration
		ok   bool
	}{
		{"", 0, false},
		{"0", 0, true},
		{" 5 ", 5 * time.Second, true},
		{"-1", 0, false},
		{"soon", 0, false},
		{"1.5", 0, false},
		{now.Add(90 * time.Second).Format(http.TimeFormat), 90 * time.Second, true},
		{now.Add(-time.Hour).Format(http.TimeFormat), 0, true},
		{"Friday, 11-Sep-26 12:00:10 GMT", 10 * time.Second, true}, // RFC 850 form, accepted by RFC 9110
	}
	for _, tt := range tests {
		got, ok := parseRetryAfter(tt.in, now)
		if got != tt.want || ok != tt.ok {
			t.Errorf("parseRetryAfter(%q) = %v, %t; want %v, %t", tt.in, got, ok, tt.want, tt.ok)
		}
	}
}

func TestParseRateLimit(t *testing.T) {
	tests := []struct {
		name string
		h    http.Header
		want *RateLimit
	}{
		{"all headers", header("RateLimit-Limit", "100", "RateLimit-Remaining", "42", "RateLimit-Reset", "30"),
			&RateLimit{Limit: 100, Remaining: 42, Reset: 30 * time.Second}},
		{"no reset", header("RateLimit-Limit", "100", "RateLimit-Remaining", "0"),
			&RateLimit{Limit: 100}},
		{"invalid reset", header("RateLimit-Limit", "100", "RateLimit-Remaining", "1", "RateLimit-Reset", "x"),
			&RateLimit{Limit: 100, Remaining: 1}},
		{"no headers", http.Header{}, nil},
		{"missing remaining", header("RateLimit-Limit", "100"), nil},
		{"negative", header("RateLimit-Limit", "100", "RateLimit-Remaining", "-1"), nil},
		{"garbage", header("RateLimit-Limit", "lots", "RateLimit-Remaining", "1"), nil},
	}
	for _, tt := range tests {
		got := parseRateLimit(tt.h)
		if (got == nil) != (tt.want == nil) || (got != nil && *got != *tt.want) {
			t.Errorf("%s: got %+v, want %+v", tt.name, got, tt.want)
		}
	}
}

func TestExecutionError(t *testing.T) {
	ms := int64(2000)
	failed := &platform.Execution{
		ExecutionID: "exec_1",
		Status:      platform.ExecutionFailed,
		Error:       &platform.Error{Code: platform.CodeTimeout, Message: "slow", Retryable: true, RetryAfterMs: &ms},
	}
	err := ExecutionError(failed)
	var e *Error
	if !errors.As(err, &e) || e.Code != platform.CodeTimeout || !e.Retryable || e.RetryAfter != 2*time.Second ||
		e.ExecutionID != "exec_1" || e.StatusCode != 0 {
		t.Errorf("ExecutionError(failed) = %+v", err)
	}
	if got := RetryAfter(fmt.Errorf("wrapped: %w", err)); got != 2*time.Second {
		t.Errorf("RetryAfter through wrapping = %v", got)
	}

	noError := &platform.Execution{ExecutionID: "exec_2", Status: platform.ExecutionFailed}
	if !errors.As(ExecutionError(noError), &e) || e.Code != CodeUnexpectedResponse || e.Retryable {
		t.Errorf("ExecutionError(failed without error) = %+v", e)
	}
	for _, s := range []platform.ExecutionStatus{platform.ExecutionSucceeded, platform.ExecutionCancelled, platform.ExecutionRunning} {
		if err := ExecutionError(&platform.Execution{Status: s}); err != nil {
			t.Errorf("ExecutionError(%s) = %v", s, err)
		}
	}
	if ExecutionError(nil) != nil {
		t.Error("ExecutionError(nil) != nil")
	}
}

func TestIsRetryable(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want bool
	}{
		{"nil", nil, false},
		{"plain error", errors.New("x"), false},
		{"retryable", &Error{Retryable: true}, true},
		{"non-retryable", &Error{}, false},
		{"wrapped retryable", fmt.Errorf("ctx: %w", &Error{Retryable: true}), true},
		{"caller cancelled", fmt.Errorf("client: %w", context.Canceled), false},
	}
	for _, tt := range tests {
		if got := IsRetryable(tt.err); got != tt.want {
			t.Errorf("%s: IsRetryable = %t", tt.name, got)
		}
	}
}

func TestTransportErrorClassification(t *testing.T) {
	req := request{method: http.MethodGet, path: "/agents"}
	cause := errors.New("connection reset")

	err := transportError(context.Background(), req, cause)
	var e *Error
	if !errors.As(err, &e) || e.Code != CodeTransport || !e.Retryable || !errors.Is(err, cause) {
		t.Errorf("transport error = %+v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	err = transportError(ctx, req, cause)
	if IsRetryable(err) || !errors.Is(err, context.Canceled) {
		t.Errorf("error after caller cancel = %v", err)
	}
}

func TestErrorString(t *testing.T) {
	e := &Error{StatusCode: 429, Code: platform.CodeQuotaExceeded, Message: "slow down"}
	if got, want := e.Error(), "agent platform: HTTP 429: QUOTA_EXCEEDED: slow down"; got != want {
		t.Errorf("got %q, want %q", got, want)
	}
	e = &Error{ExecutionID: "exec_1", Code: platform.CodeTimeout, Message: "late"}
	if got, want := e.Error(), "agent platform: execution exec_1: TIMEOUT: late"; got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}
