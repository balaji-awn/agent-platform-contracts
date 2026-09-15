package client_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/sbalaji6/agent-platform-contracts/client"
	"github.com/sbalaji6/agent-platform-contracts/mock"
	"github.com/sbalaji6/agent-platform-contracts/platform"
)

// These tests run the client against the mock platform from Step 1.

func setup(t *testing.T, opts mock.Options, cfg client.Config) (*mock.Server, *client.TenantClient) {
	t.Helper()
	srv := mock.NewTestServer(t, opts)
	if err := srv.AddAgentVersion(mock.AlertTriage()); err != nil {
		t.Fatal(err)
	}
	cfg.BaseURL = srv.URL
	if cfg.Token == nil {
		cfg.Token = client.StaticToken(opts.Token)
	}
	c, err := client.New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	return srv, c.Tenant("t1")
}

func execRequest(runID string) platform.ExecutionRequest {
	return platform.ExecutionRequest{
		AgentID:      "alert-triage",
		AgentVersion: 3,
		Input:        json.RawMessage(`{"alert":{"id":"a1"}}`),
		Context:      platform.ExecutionContext{WorkflowID: "wf_phishing@7", RunID: runID, NodePath: "triage_agent", Attempt: 1},
		TimeoutMs:    60000,
	}
}

func asError(t *testing.T, err error) *client.Error {
	t.Helper()
	var e *client.Error
	if !errors.As(err, &e) {
		t.Fatalf("error %v (%T) is not a *client.Error", err, err)
	}
	return e
}

func poll(t *testing.T, tc *client.TenantClient, id string) *platform.Execution {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for {
		x, _, err := tc.GetExecution(context.Background(), id)
		if err != nil {
			t.Fatal(err)
		}
		if x.Status.Terminal() {
			return x
		}
		if time.Now().After(deadline) {
			t.Fatalf("execution %s still %s", id, x.Status)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestNewRejectsBadBaseURL(t *testing.T) {
	for _, u := range []string{"", "agent-platform/v1", "ftp://x", "http://"} {
		if _, err := client.New(client.Config{BaseURL: u}); err == nil {
			t.Errorf("New(%q) succeeded", u)
		}
	}
}

func TestCatalog(t *testing.T) {
	_, tc := setup(t, mock.Options{}, client.Config{})
	ctx := context.Background()

	list, resp, err := tc.ListAgents(ctx, client.ListAgentsParams{Search: "triage", Tags: []string{"security", "triage"}, Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if len(list.Items) != 1 || list.Items[0].AgentID != "alert-triage" || list.NextCursor != nil {
		t.Errorf("list %+v", list)
	}
	if resp.RateLimit == nil || resp.RateLimit.Limit != 10000 || resp.RateLimit.Remaining != 9999 {
		t.Errorf("rate limit %+v", resp.RateLimit)
	}

	agent, _, err := tc.GetAgent(ctx, "alert-triage")
	if err != nil || agent.LatestVersion == nil || *agent.LatestVersion != 3 {
		t.Fatalf("GetAgent = %+v, %v", agent, err)
	}
	versions, _, err := tc.ListAgentVersions(ctx, "alert-triage")
	if err != nil || len(versions.Items) != 1 || versions.Items[0].Status != platform.VersionPublished {
		t.Fatalf("ListAgentVersions = %+v, %v", versions, err)
	}
	contract, _, err := tc.GetAgentVersion(ctx, "alert-triage", 3)
	if err != nil {
		t.Fatal(err)
	}
	if d, err := contract.ComputeDigest(); err != nil || d != contract.Digest || d != versions.Items[0].Digest {
		t.Errorf("digest %s, computed %s (%v)", contract.Digest, d, err)
	}

	_, resp, err = tc.GetAgent(ctx, "no/such agent")
	e := asError(t, err)
	if e.StatusCode != 404 || e.Code != platform.CodeAgentNotFound || e.Retryable || resp.StatusCode != 404 {
		t.Errorf("GetAgent(unknown) = %+v", e)
	}
}

func TestSyncSuccess(t *testing.T) {
	srv, tc := setup(t, mock.Options{Latency: 20 * time.Millisecond}, client.Config{})
	x, resp, err := tc.CreateExecution(context.Background(), "run1/triage_agent/1", execRequest("run1"), 5*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != 200 || resp.Accepted() || x.Status != platform.ExecutionSucceeded || client.ExecutionError(x) != nil {
		t.Fatalf("status %d, execution %+v", resp.StatusCode, x)
	}
	if x.Usage == nil || x.LatencyMs == nil || x.TraceID == "" || len(x.Output) == 0 {
		t.Errorf("incomplete execution %+v", x)
	}
	calls := srv.Calls(mock.OpCreateExecution)
	if len(calls) != 1 || calls[0].TenantID != "t1" || calls[0].IdempotencyKey != "run1/triage_agent/1" {
		t.Errorf("calls %+v", calls)
	}
}

func TestAcceptedThenPoll(t *testing.T) {
	srv, tc := setup(t, mock.Options{MaxWait: 20 * time.Millisecond}, client.Config{})
	srv.Script(mock.Match{}, mock.Succeed(map[string]string{"severity": "low", "verdict": "benign"}).After(150*time.Millisecond))

	x, resp, err := tc.CreateExecution(context.Background(), "k1", execRequest("run1"), 10*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if !resp.Accepted() || resp.Location() != "/executions/"+x.ExecutionID || x.Status.Terminal() {
		t.Fatalf("status %d, Location %q, execution %+v", resp.StatusCode, resp.Location(), x)
	}
	final := poll(t, tc, x.ExecutionID)
	if final.Status != platform.ExecutionSucceeded || string(final.Output) != `{"severity":"low","verdict":"benign"}` {
		t.Errorf("final %+v", final)
	}
}

func TestRetryableExecutionFailure(t *testing.T) {
	srv, tc := setup(t, mock.Options{}, client.Config{})
	srv.Script(mock.Match{}, mock.Fail(platform.CodeProviderUnavailable).WithRetryAfter(3*time.Second))

	x, resp, err := tc.CreateExecution(context.Background(), "k1", execRequest("run1"), 5*time.Second)
	if err != nil {
		t.Fatalf("a failed execution must not be a Go error: %v", err)
	}
	if resp.StatusCode != 200 || x.Status != platform.ExecutionFailed {
		t.Fatalf("status %d, execution %+v", resp.StatusCode, x)
	}
	e := asError(t, client.ExecutionError(x))
	if e.Code != platform.CodeProviderUnavailable || !e.Retryable || e.RetryAfter != 3*time.Second || e.ExecutionID != x.ExecutionID {
		t.Errorf("execution error %+v", e)
	}
}

func TestNonRetryableFailures(t *testing.T) {
	srv, tc := setup(t, mock.Options{}, client.Config{})
	ctx := context.Background()

	bad := execRequest("run1")
	bad.Input = json.RawMessage(`{}`)
	_, resp, err := tc.CreateExecution(ctx, "k1", bad, 5*time.Second)
	e := asError(t, err)
	if e.StatusCode != 400 || e.Code != platform.CodeInputSchemaInvalid || e.Retryable || len(e.Details) != 1 || resp == nil {
		t.Errorf("invalid input: %+v", e)
	}

	srv.Script(mock.Match{}, mock.Fail(platform.CodeOutputSchemaViolation))
	x, _, err := tc.CreateExecution(ctx, "k2", execRequest("run1"), 5*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if e := asError(t, client.ExecutionError(x)); e.Code != platform.CodeOutputSchemaViolation || e.Retryable {
		t.Errorf("output violation: %+v", e)
	}

	srv.SetVersionStatus("alert-triage", 3, platform.VersionDisabled)
	_, _, err = tc.CreateExecution(ctx, "k3", execRequest("run1"), 0)
	if e := asError(t, err); e.StatusCode != 409 || e.Code != platform.CodeAgentVersionDisabled || client.IsRetryable(err) {
		t.Errorf("disabled: %+v", e)
	}
}

func TestRetryableRejection(t *testing.T) {
	srv, tc := setup(t, mock.Options{}, client.Config{})
	srv.Script(mock.Match{}, mock.Reject(platform.CodeQuotaExceeded).WithRetryAfter(1500*time.Millisecond))

	_, resp, err := tc.CreateExecution(context.Background(), "k1", execRequest("run1"), 5*time.Second)
	e := asError(t, err)
	// The body says 1500ms and the header says 2s; the body is authoritative.
	if e.StatusCode != 429 || e.Code != platform.CodeQuotaExceeded || !e.Retryable || e.RetryAfter != 1500*time.Millisecond {
		t.Errorf("rejection %+v", e)
	}
	if resp.Header.Get("Retry-After") != "2" || e.RateLimit == nil {
		t.Errorf("Retry-After %q, rate limit %+v", resp.Header.Get("Retry-After"), e.RateLimit)
	}

	// The key was not recorded, so the retry with the same key starts the execution.
	x, _, err := tc.CreateExecution(context.Background(), "k1", execRequest("run1"), 5*time.Second)
	if err != nil || x.Status != platform.ExecutionSucceeded || len(srv.Executions()) != 1 {
		t.Errorf("retry = %+v, %v; %d executions", x, err, len(srv.Executions()))
	}
}

func TestTimeout(t *testing.T) {
	srv, tc := setup(t, mock.Options{TimeoutScale: 0.01}, client.Config{})
	srv.Script(mock.Match{}, mock.Hang())
	req := execRequest("run1")
	req.TimeoutMs = 1000

	x, _, err := tc.CreateExecution(context.Background(), "k1", req, 5*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	e := asError(t, client.ExecutionError(x))
	if e.Code != platform.CodeTimeout || !e.Retryable {
		t.Errorf("timeout error %+v", e)
	}
}

func TestCancel(t *testing.T) {
	srv, tc := setup(t, mock.Options{}, client.Config{})
	srv.Script(mock.Match{}, mock.Hang().IgnoreTimeout())
	ctx := context.Background()

	x, resp, err := tc.CreateExecution(ctx, "k1", execRequest("run1"), 0)
	if err != nil || !resp.Accepted() {
		t.Fatalf("create = %+v, %v", resp, err)
	}
	c, resp, err := tc.CancelExecution(ctx, x.ExecutionID)
	if err != nil || resp.StatusCode != 202 || c.Status != platform.ExecutionCancelled || client.ExecutionError(c) != nil {
		t.Fatalf("cancel = %+v, %+v, %v", c, resp, err)
	}
	if rec, _ := srv.Execution(x.ExecutionID); rec.CancelCalls != 1 {
		t.Errorf("CancelCalls %d", rec.CancelCalls)
	}
	_, _, err = tc.CancelExecution(ctx, "nope")
	if e := asError(t, err); e.Code != platform.CodeExecutionNotFound {
		t.Errorf("cancel unknown: %+v", e)
	}
}

func TestIdempotentReplay(t *testing.T) {
	srv, tc := setup(t, mock.Options{Latency: 100 * time.Millisecond}, client.Config{})
	ctx := context.Background()

	first, resp, err := tc.CreateExecution(ctx, "k1", execRequest("run1"), 0)
	if err != nil || !resp.Accepted() {
		t.Fatalf("first = %+v, %v", resp, err)
	}
	// A redelivered River job sends the same key and body.
	again, resp, err := tc.CreateExecution(ctx, "k1", execRequest("run1"), 5*time.Second)
	if err != nil || resp.StatusCode != 200 || again.ExecutionID != first.ExecutionID {
		t.Fatalf("replay = %+v, %+v, %v", again, resp, err)
	}
	if execs := srv.Executions(); len(execs) != 1 || execs[0].Replays != 1 {
		t.Errorf("executions %+v", execs)
	}

	_, _, err = tc.CreateExecution(ctx, "k1", execRequest("run2"), 0)
	if e := asError(t, err); e.StatusCode != 409 || e.Code != platform.CodeIdempotencyKeyReused || e.Retryable {
		t.Errorf("reused key: %+v", e)
	}
	if _, _, err := tc.CreateExecution(ctx, "", execRequest("run1"), 0); err == nil {
		t.Error("empty idempotency key accepted")
	}
}

func TestAuth(t *testing.T) {
	_, tc := setup(t, mock.Options{Token: "s3cret"}, client.Config{})
	if _, _, err := tc.ListAgents(context.Background(), client.ListAgentsParams{}); err != nil {
		t.Fatalf("with token: %v", err)
	}
	_, bad := setup(t, mock.Options{Token: "s3cret"}, client.Config{Token: client.StaticToken("wrong")})
	_, _, err := bad.ListAgents(context.Background(), client.ListAgentsParams{})
	if e := asError(t, err); e.StatusCode != 401 || e.Code != platform.CodeUnauthorized || e.Retryable {
		t.Errorf("wrong token: %+v", e)
	}
}

func TestRequestTimeoutIsRetryable(t *testing.T) {
	srv, tc := setup(t, mock.Options{}, client.Config{Timeout: 50 * time.Millisecond})
	srv.FailRequests(mock.OpGetAgent, mock.Reject(platform.CodeProviderUnavailable).After(2*time.Second))

	start := time.Now()
	_, resp, err := tc.GetAgent(context.Background(), "alert-triage")
	e := asError(t, err)
	if e.Code != client.CodeTransport || !e.Retryable || resp != nil || !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("timed-out request: %+v (resp %v)", e, resp)
	}
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Errorf("took %v, want about 50ms", elapsed)
	}
}

func TestWaitExtendsRequestTimeout(t *testing.T) {
	// Timeout is 50ms, but a call that waits is bounded by wait + WaitMargin instead.
	_, tc := setup(t, mock.Options{Latency: 200 * time.Millisecond}, client.Config{Timeout: 50 * time.Millisecond})
	x, resp, err := tc.CreateExecution(context.Background(), "k1", execRequest("run1"), time.Second)
	if err != nil || resp.StatusCode != 200 || x.Status != platform.ExecutionSucceeded {
		t.Fatalf("create = %+v, %+v, %v", x, resp, err)
	}
}

func TestCallerCancelIsNotRetryable(t *testing.T) {
	_, tc := setup(t, mock.Options{}, client.Config{})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, _, err := tc.GetAgent(ctx, "alert-triage")
	if err == nil || client.IsRetryable(err) || !errors.Is(err, context.Canceled) {
		t.Errorf("err = %v, retryable %t", err, client.IsRetryable(err))
	}
}

func TestPollFailureIsRetryable(t *testing.T) {
	srv, tc := setup(t, mock.Options{}, client.Config{})
	x, _, err := tc.CreateExecution(context.Background(), "k1", execRequest("run1"), 5*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	srv.FailRequests(mock.OpGetExecution, mock.Reject(platform.CodeProviderUnavailable))
	_, _, err = tc.GetExecution(context.Background(), x.ExecutionID)
	if e := asError(t, err); e.StatusCode != 503 || !e.Retryable || e.RetryAfter != time.Second {
		t.Errorf("poll failure: %+v", e)
	}
}

// Also covers a server mounted under a base path, which the Location header and every request path
// must respect.
func TestTraceWithBasePath(t *testing.T) {
	_, tc := setup(t, mock.Options{BasePath: "/v1"}, client.Config{})
	ctx := context.Background()
	x, _, err := tc.CreateExecution(ctx, "k1", execRequest("run1"), 5*time.Second)
	if err != nil || x.Status != platform.ExecutionSucceeded {
		t.Fatalf("create = %+v, %v", x, err)
	}
	tr, _, err := tc.GetExecutionTrace(ctx, x.ExecutionID)
	if err != nil || tr.ExecutionID != x.ExecutionID || len(tr.Steps) == 0 {
		t.Errorf("trace = %+v, %v", tr, err)
	}
}

func TestUnexpectedResponses(t *testing.T) {
	hs := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/agents/html" {
			w.WriteHeader(http.StatusBadGateway)
			w.Write([]byte("<html>Bad Gateway</html>"))
			return
		}
		w.Write([]byte("not json"))
	}))
	defer hs.Close()
	c, err := client.New(client.Config{BaseURL: hs.URL})
	if err != nil {
		t.Fatal(err)
	}
	tc := c.Tenant("t1")

	_, _, err = tc.GetAgent(context.Background(), "html")
	if e := asError(t, err); e.StatusCode != 502 || e.Code != client.CodeUnexpectedResponse || !e.Retryable {
		t.Errorf("HTML 502: %+v", e)
	}
	_, _, err = tc.GetAgent(context.Background(), "garbage")
	if e := asError(t, err); e.StatusCode != 200 || e.Code != client.CodeUnexpectedResponse || e.Retryable {
		t.Errorf("undecodable 200: %+v", e)
	}
	if _, _, err := c.Tenant("").GetAgent(context.Background(), "x"); err == nil {
		t.Error("empty tenant accepted")
	}
}
