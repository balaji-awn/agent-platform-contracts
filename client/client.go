// Package client is a Go client for the agent platform API (api/openapi.yaml).
//
//	c, err := client.New(client.Config{BaseURL: "https://agent-platform.internal/v1", Token: client.StaticToken(token)})
//	tc := c.Tenant(tenantID)
//	x, resp, err := tc.CreateExecution(ctx, idempotencyKey, req, 20*time.Second)
//	switch {
//	case err != nil:                    // request-level rejection or transport failure; see IsRetryable
//	case resp.Accepted():               // still running; poll tc.GetExecution(ctx, x.ExecutionID)
//	case client.ExecutionError(x) != nil: // the execution failed; same *Error type as rejections
//	}
//
// The client never retries. Callers own retries and decide with IsRetryable and RetryAfter, so
// retries are not multiplied across layers. A failed execution is a result, not an error:
// CreateExecution and GetExecution return it with a nil error, and ExecutionError converts it into
// the *Error that request-level rejections use.
package client

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/sbalaji6/agent-platform-contracts/platform"
)

const (
	// DefaultTimeout bounds requests that do not wait for an execution.
	DefaultTimeout = 30 * time.Second
	// DefaultWaitMargin is added to the Prefer: wait window to bound requests that wait.
	DefaultWaitMargin = 5 * time.Second

	maxResponseBytes = 32 << 20
)

// TokenSource supplies the bearer token for each request.
type TokenSource interface {
	Token(ctx context.Context) (string, error)
}

// StaticToken is a TokenSource that always returns the same token.
type StaticToken string

// Token implements TokenSource.
func (t StaticToken) Token(context.Context) (string, error) { return string(t), nil }

// Config configures a Client.
type Config struct {
	// BaseURL is the API root, including any version prefix, e.g. https://agent-platform.internal/v1.
	BaseURL string
	// Token supplies the bearer token. Nil sends no Authorization header.
	Token TokenSource
	// HTTPClient sends requests. Default: a new http.Client with no overall timeout, since
	// per-request timeouts come from Timeout and WaitMargin.
	HTTPClient *http.Client
	// Timeout bounds each request that does not wait for an execution. Default DefaultTimeout.
	Timeout time.Duration
	// WaitMargin is added to the wait window to bound CreateExecution requests.
	// Default DefaultWaitMargin.
	WaitMargin time.Duration
	// UserAgent, if set, is sent as the User-Agent header.
	UserAgent string
}

// Client calls the agent platform API. It is safe for concurrent use.
type Client struct {
	base string
	cfg  Config
	hc   *http.Client
}

// New returns a Client for cfg.
func New(cfg Config) (*Client, error) {
	u, err := url.Parse(cfg.BaseURL)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return nil, fmt.Errorf("client: BaseURL must be an absolute http or https URL, got %q", cfg.BaseURL)
	}
	if cfg.Timeout <= 0 {
		cfg.Timeout = DefaultTimeout
	}
	if cfg.WaitMargin <= 0 {
		cfg.WaitMargin = DefaultWaitMargin
	}
	hc := cfg.HTTPClient
	if hc == nil {
		hc = &http.Client{}
	}
	return &Client{base: strings.TrimRight(cfg.BaseURL, "/"), cfg: cfg, hc: hc}, nil
}

// TenantClient makes calls on behalf of one tenant. Get one from Client.Tenant.
type TenantClient struct {
	c        *Client
	tenantID string
}

// Tenant returns a TenantClient whose requests carry X-Tenant-ID: tenantID. It is cheap; create one
// per call site or per request.
func (c *Client) Tenant(tenantID string) *TenantClient {
	return &TenantClient{c: c, tenantID: tenantID}
}

// Response is the HTTP response metadata of a call. Methods return it even when they return an
// error, whenever the platform answered.
type Response struct {
	StatusCode int
	Header     http.Header
	// RateLimit is parsed from the RateLimit-* headers, or nil if they are absent.
	RateLimit *RateLimit
}

// Accepted reports whether the platform answered 202: the execution is still running.
func (r *Response) Accepted() bool {
	return r != nil && r.StatusCode == http.StatusAccepted
}

// Location returns the Location header of a 202, a URI reference to the execution.
func (r *Response) Location() string {
	if r == nil {
		return ""
	}
	return r.Header.Get("Location")
}

type request struct {
	method         string
	path           string
	query          url.Values
	body           any
	idempotencyKey string
	wait           time.Duration
}

func (t *TenantClient) do(ctx context.Context, req request, out any) (*Response, error) {
	if t.tenantID == "" {
		return nil, errors.New("client: tenant ID is empty")
	}
	timeout := t.c.cfg.Timeout
	waitSeconds := int(math.Ceil(req.wait.Seconds()))
	if waitSeconds > 0 {
		timeout = time.Duration(waitSeconds)*time.Second + t.c.cfg.WaitMargin
	}
	rctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	var body io.Reader
	if req.body != nil {
		b, err := json.Marshal(req.body)
		if err != nil {
			return nil, fmt.Errorf("client: encoding %s %s: %w", req.method, req.path, err)
		}
		body = bytes.NewReader(b)
	}
	target := t.c.base + req.path
	if len(req.query) > 0 {
		target += "?" + req.query.Encode()
	}
	hr, err := http.NewRequestWithContext(rctx, req.method, target, body)
	if err != nil {
		return nil, fmt.Errorf("client: %w", err)
	}
	hr.Header.Set("Accept", "application/json")
	if body != nil {
		hr.Header.Set("Content-Type", "application/json")
	}
	hr.Header.Set(platform.HeaderTenantID, t.tenantID)
	if t.c.cfg.UserAgent != "" {
		hr.Header.Set("User-Agent", t.c.cfg.UserAgent)
	}
	if req.idempotencyKey != "" {
		hr.Header.Set(platform.HeaderIdempotencyKey, req.idempotencyKey)
	}
	if waitSeconds > 0 {
		hr.Header.Set(platform.HeaderPrefer, fmt.Sprintf("wait=%d", waitSeconds))
	}
	if t.c.cfg.Token != nil {
		tok, err := t.c.cfg.Token.Token(rctx)
		if err != nil {
			return nil, fmt.Errorf("client: getting token: %w", err)
		}
		hr.Header.Set("Authorization", "Bearer "+tok)
	}

	resp, err := t.c.hc.Do(hr)
	if err != nil {
		return nil, transportError(ctx, req, err)
	}
	defer resp.Body.Close()
	r := &Response{StatusCode: resp.StatusCode, Header: resp.Header, RateLimit: parseRateLimit(resp.Header)}
	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes))
	if err != nil {
		return r, transportError(ctx, req, err)
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return r, decodeError(resp.StatusCode, resp.Header, raw, r.RateLimit, time.Now())
	}
	if out != nil {
		if err := json.Unmarshal(raw, out); err != nil {
			return r, &Error{
				StatusCode: resp.StatusCode,
				Code:       CodeUnexpectedResponse,
				Message:    "decoding response body: " + err.Error(),
				RateLimit:  r.RateLimit,
				cause:      err,
			}
		}
	}
	return r, nil
}

// transportError classifies a failure to send a request or read its response. If the caller's
// context ended, the context error is returned so it is not retried; otherwise the failure,
// including the client's own per-request timeout, is a retryable *Error.
func transportError(ctx context.Context, req request, err error) error {
	if ctxErr := ctx.Err(); ctxErr != nil {
		return fmt.Errorf("client: %s %s: %w", req.method, req.path, ctxErr)
	}
	return &Error{
		Code:      CodeTransport,
		Message:   fmt.Sprintf("%s %s: %v", req.method, req.path, err),
		Retryable: true,
		cause:     err,
	}
}

func call[T any](ctx context.Context, t *TenantClient, req request) (*T, *Response, error) {
	out := new(T)
	resp, err := t.do(ctx, req, out)
	if err != nil {
		return nil, resp, err
	}
	return out, resp, nil
}
