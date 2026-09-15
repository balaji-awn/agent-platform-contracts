package mock_test

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/sbalaji6/agent-platform-contracts/mock"
	"github.com/sbalaji6/agent-platform-contracts/platform"
)

type harness struct {
	t   *testing.T
	srv *mock.Server
}

func newHarness(t *testing.T, opts mock.Options) *harness {
	t.Helper()
	srv := mock.NewTestServer(t, opts)
	if err := srv.AddAgentVersion(mock.AlertTriage()); err != nil {
		t.Fatal(err)
	}
	return &harness{t: t, srv: srv}
}

// do sends a request as tenant t1. headers are name/value pairs; an empty value deletes the header.
func (h *harness) do(method, path string, body any, headers ...string) (*http.Response, []byte) {
	h.t.Helper()
	var r io.Reader
	switch b := body.(type) {
	case nil:
	case string:
		r = strings.NewReader(b)
	default:
		raw, err := json.Marshal(b)
		if err != nil {
			h.t.Fatal(err)
		}
		r = bytes.NewReader(raw)
	}
	req, err := http.NewRequest(method, h.srv.URL+path, r)
	if err != nil {
		h.t.Fatal(err)
	}
	req.Header.Set(platform.HeaderTenantID, "t1")
	for i := 0; i+1 < len(headers); i += 2 {
		if headers[i+1] == "" {
			req.Header.Del(headers[i])
		} else {
			req.Header.Set(headers[i], headers[i+1])
		}
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		h.t.Fatal(err)
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		h.t.Fatal(err)
	}
	return resp, data
}

func (h *harness) create(key string, body any, headers ...string) (*http.Response, []byte) {
	h.t.Helper()
	return h.do(http.MethodPost, "/executions", body, append([]string{platform.HeaderIdempotencyKey, key}, headers...)...)
}

func (h *harness) poll(id string) platform.Execution {
	h.t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for {
		resp, body := h.do(http.MethodGet, "/executions/"+id, nil)
		wantStatus(h.t, resp, body, http.StatusOK)
		x := decode[platform.Execution](h.t, body)
		if x.Status.Terminal() {
			return x
		}
		if time.Now().After(deadline) {
			h.t.Fatalf("execution %s still %s after 3s", id, x.Status)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func execBody(runID string) map[string]any {
	return map[string]any{
		"agent_id":      "alert-triage",
		"agent_version": 3,
		"input":         map[string]any{"alert": map[string]any{"id": "a1"}},
		"context": map[string]any{
			"workflow_id": "wf_phishing@7",
			"run_id":      runID,
			"node_path":   "triage_agent",
			"attempt":     1,
		},
		"timeout_ms": 60000,
	}
}

func decode[T any](t *testing.T, data []byte) T {
	t.Helper()
	var v T
	if err := json.Unmarshal(data, &v); err != nil {
		t.Fatalf("decoding %s: %v", data, err)
	}
	return v
}

func wantStatus(t *testing.T, resp *http.Response, body []byte, status int) {
	t.Helper()
	if resp.StatusCode != status {
		t.Fatalf("status %d, want %d; body %s", resp.StatusCode, status, body)
	}
}

func wantError(t *testing.T, resp *http.Response, body []byte, status int, code platform.ErrorCode) platform.Error {
	t.Helper()
	wantStatus(t, resp, body, status)
	env := decode[platform.ErrorEnvelope](t, body)
	if env.Error.Code != code {
		t.Fatalf("code %s, want %s; body %s", env.Error.Code, code, body)
	}
	return env.Error
}

func TestSyncSuccess(t *testing.T) {
	h := newHarness(t, mock.Options{})
	resp, body := h.create("k1", execBody("run1"), platform.HeaderPrefer, "wait=5")
	wantStatus(t, resp, body, http.StatusOK)
	x := decode[platform.Execution](t, body)
	if x.Status != platform.ExecutionSucceeded {
		t.Fatalf("status %s, want succeeded", x.Status)
	}
	if got, want := string(x.Output), `{"severity":"high","verdict":"suspicious","reasoning":"Mock verdict."}`; got != want {
		t.Errorf("output %s, want %s", got, want)
	}
	if x.Usage == nil || x.LatencyMs == nil || x.FinishedAt == nil || x.Model == nil || x.TraceID == "" {
		t.Errorf("incomplete execution: %s", body)
	}
	if got := resp.Header.Get(platform.HeaderPreferenceApplied); got != "wait=5" {
		t.Errorf("Preference-Applied %q, want wait=5", got)
	}
	for _, name := range []string{platform.HeaderRateLimitLimit, platform.HeaderRateLimitRemaining, platform.HeaderRateLimitReset} {
		if resp.Header.Get(name) == "" {
			t.Errorf("missing %s header", name)
		}
	}
}

func TestSlowSuccessAcceptedThenPoll(t *testing.T) {
	h := newHarness(t, mock.Options{MaxWait: 20 * time.Millisecond})
	h.srv.Script(mock.Match{}, mock.Succeed(map[string]string{"severity": "low", "verdict": "benign"}).After(150*time.Millisecond))

	resp, body := h.create("k1", execBody("run1"), platform.HeaderPrefer, "wait=10")
	wantStatus(t, resp, body, http.StatusAccepted)
	x := decode[platform.Execution](t, body)
	if x.Status != platform.ExecutionRunning || x.FinishedAt != nil {
		t.Fatalf("accepted execution: %s", body)
	}
	if got := resp.Header.Get("Location"); got != "/executions/"+x.ExecutionID {
		t.Errorf("Location %q", got)
	}
	if got := resp.Header.Get(platform.HeaderPreferenceApplied); got != "wait=0" {
		t.Errorf("Preference-Applied %q, want wait=0 after the 20ms cap", got)
	}

	final := h.poll(x.ExecutionID)
	if final.Status != platform.ExecutionSucceeded || string(final.Output) != `{"severity":"low","verdict":"benign"}` {
		t.Errorf("final execution %+v, output %s", final, final.Output)
	}
}

func TestNoPreferDoesNotWait(t *testing.T) {
	h := newHarness(t, mock.Options{Latency: 100 * time.Millisecond})
	resp, body := h.create("k1", execBody("run1"))
	wantStatus(t, resp, body, http.StatusAccepted)
	if resp.Header.Get(platform.HeaderPreferenceApplied) != "" {
		t.Error("Preference-Applied sent without Prefer")
	}
}

func TestExecutionLevelFailures(t *testing.T) {
	ms := func(n int64) *int64 { return &n }
	tests := []struct {
		name       string
		outcome    mock.Outcome
		code       platform.ErrorCode
		retryable  bool
		retryAfter *int64
		message    string
	}{
		{"retryable", mock.Fail(platform.CodeProviderUnavailable), platform.CodeProviderUnavailable, true, ms(1000), "mock: PROVIDER_UNAVAILABLE"},
		{"non-retryable", mock.Fail(platform.CodeOutputSchemaViolation), platform.CodeOutputSchemaViolation, false, nil, "mock: OUTPUT_SCHEMA_VIOLATION"},
		{"overrides", mock.Fail(platform.CodeProviderRateLimited).WithRetryAfter(5 * time.Second).WithMessage("slow down"), platform.CodeProviderRateLimited, true, ms(5000), "slow down"},
		{"flag override", mock.Fail(platform.CodeInternal).WithRetryable(false), platform.CodeInternal, false, nil, "mock: INTERNAL"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := newHarness(t, mock.Options{})
			h.srv.Script(mock.Match{}, tt.outcome)
			resp, body := h.create("k1", execBody("run1"), platform.HeaderPrefer, "wait=5")
			wantStatus(t, resp, body, http.StatusOK)
			x := decode[platform.Execution](t, body)
			if x.Status != platform.ExecutionFailed || x.Error == nil || x.Output != nil {
				t.Fatalf("execution: %s", body)
			}
			e := x.Error
			if e.Code != tt.code || e.Retryable != tt.retryable || e.Message != tt.message {
				t.Errorf("error %+v", e)
			}
			if (e.RetryAfterMs == nil) != (tt.retryAfter == nil) || (e.RetryAfterMs != nil && *e.RetryAfterMs != *tt.retryAfter) {
				t.Errorf("retry_after_ms %v, want %v", e.RetryAfterMs, tt.retryAfter)
			}
		})
	}
}

func TestRetryableRejectionDoesNotRecordKey(t *testing.T) {
	h := newHarness(t, mock.Options{})
	h.srv.Script(mock.Match{}, mock.Reject(platform.CodeQuotaExceeded).WithRetryAfter(2500*time.Millisecond))

	resp, body := h.create("k1", execBody("run1"), platform.HeaderPrefer, "wait=5")
	e := wantError(t, resp, body, http.StatusTooManyRequests, platform.CodeQuotaExceeded)
	if !e.Retryable || e.RetryAfterMs == nil || *e.RetryAfterMs != 2500 {
		t.Errorf("error %+v", e)
	}
	if got := resp.Header.Get(platform.HeaderRetryAfter); got != "3" {
		t.Errorf("Retry-After %q, want 3", got)
	}
	if n := len(h.srv.Executions()); n != 0 {
		t.Fatalf("%d executions after a rejection, want 0", n)
	}

	resp, body = h.create("k1", execBody("run1"), platform.HeaderPrefer, "wait=5")
	wantStatus(t, resp, body, http.StatusOK)
	if n := len(h.srv.Executions()); n != 1 {
		t.Errorf("%d executions, want 1", n)
	}
}

func TestNonRetryableRejections(t *testing.T) {
	tests := []struct {
		name    string
		setup   func(*mock.Server)
		modify  func(map[string]any)
		headers []string
		raw     string
		status  int
		code    platform.ErrorCode
		detail  string
	}{
		{name: "input schema", modify: func(b map[string]any) { b["input"] = map[string]any{} },
			status: 400, code: platform.CodeInputSchemaInvalid, detail: "/alert"},
		{name: "input enum", modify: func(b map[string]any) {
			b["input"] = map[string]any{"alert": map[string]any{}, "tenant_policy": "loose"}
		},
			status: 400, code: platform.CodeInputSchemaInvalid, detail: "/tenant_policy"},
		{name: "unknown version", modify: func(b map[string]any) { b["agent_version"] = 9 },
			status: 404, code: platform.CodeAgentVersionNotFound},
		{name: "disabled version", setup: func(s *mock.Server) { s.SetVersionStatus("alert-triage", 3, platform.VersionDisabled) },
			status: 409, code: platform.CodeAgentVersionDisabled},
		{name: "invisible agent", setup: func(s *mock.Server) { s.RestrictAgent("alert-triage", "t2") },
			status: 404, code: platform.CodeAgentVersionNotFound},
		{name: "timeout above max", modify: func(b map[string]any) { b["timeout_ms"] = 200000 },
			status: 400, code: platform.CodeRequestInvalid, detail: "/timeout_ms"},
		{name: "timeout below min", modify: func(b map[string]any) { b["timeout_ms"] = 500 },
			status: 400, code: platform.CodeRequestInvalid, detail: "/timeout_ms"},
		{name: "missing idempotency key", headers: []string{platform.HeaderIdempotencyKey, ""},
			status: 400, code: platform.CodeRequestInvalid},
		{name: "unknown field", modify: func(b map[string]any) { b["priority"] = 1 },
			status: 400, code: platform.CodeRequestInvalid},
		{name: "unknown context field", modify: func(b map[string]any) { b["context"].(map[string]any)["tenant_id"] = "t1" },
			status: 400, code: platform.CodeRequestInvalid},
		{name: "missing run_id", modify: func(b map[string]any) { delete(b["context"].(map[string]any), "run_id") },
			status: 400, code: platform.CodeRequestInvalid, detail: "/context/run_id"},
		{name: "missing input", modify: func(b map[string]any) { delete(b, "input") },
			status: 400, code: platform.CodeRequestInvalid, detail: "/input"},
		{name: "not JSON", raw: "{", status: 400, code: platform.CodeRequestInvalid},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := newHarness(t, mock.Options{})
			if tt.setup != nil {
				tt.setup(h.srv)
			}
			var body any = tt.raw
			if tt.raw == "" {
				b := execBody("run1")
				if tt.modify != nil {
					tt.modify(b)
				}
				body = b
			}
			resp, data := h.create("k1", body, tt.headers...)
			e := wantError(t, resp, data, tt.status, tt.code)
			if e.Retryable {
				t.Error("retryable = true, want false")
			}
			if tt.detail != "" && (len(e.Details) == 0 || e.Details[0].Path != tt.detail) {
				t.Errorf("details %+v, want path %s", e.Details, tt.detail)
			}
			if n := len(h.srv.Executions()); n != 0 {
				t.Errorf("%d executions, want 0", n)
			}
		})
	}
}

func TestTimeout(t *testing.T) {
	tests := []struct {
		name    string
		outcome mock.Outcome
	}{
		{"hang", mock.Hang()},
		{"slower than timeout", mock.Succeed(nil).After(5 * time.Second)},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := newHarness(t, mock.Options{TimeoutScale: 0.01})
			h.srv.Script(mock.Match{}, tt.outcome)
			b := execBody("run1")
			b["timeout_ms"] = 1000 // 10ms after scaling
			start := time.Now()
			resp, body := h.create("k1", b, platform.HeaderPrefer, "wait=5")
			wantStatus(t, resp, body, http.StatusOK)
			x := decode[platform.Execution](t, body)
			if x.Status != platform.ExecutionFailed || x.Error == nil || x.Error.Code != platform.CodeTimeout || !x.Error.Retryable {
				t.Fatalf("execution: %s", body)
			}
			if elapsed := time.Since(start); elapsed > 2*time.Second {
				t.Errorf("took %v", elapsed)
			}
		})
	}
}

func TestCancel(t *testing.T) {
	h := newHarness(t, mock.Options{})
	h.srv.Script(mock.Match{}, mock.Hang().IgnoreTimeout())
	resp, body := h.create("k1", execBody("run1"))
	wantStatus(t, resp, body, http.StatusAccepted)
	id := decode[platform.Execution](t, body).ExecutionID

	for i := 0; i < 2; i++ {
		resp, body = h.do(http.MethodPost, "/executions/"+id+"/cancel", nil)
		wantStatus(t, resp, body, http.StatusAccepted)
		if x := decode[platform.Execution](t, body); x.Status != platform.ExecutionCancelled || x.Error != nil || x.FinishedAt == nil {
			t.Fatalf("cancel %d: %s", i, body)
		}
	}
	if x := h.poll(id); x.Status != platform.ExecutionCancelled {
		t.Errorf("status %s after cancel", x.Status)
	}
	rec, _ := h.srv.Execution(id)
	if rec.CancelCalls != 2 {
		t.Errorf("CancelCalls %d, want 2", rec.CancelCalls)
	}

	resp, body = h.do(http.MethodPost, "/executions/nope/cancel", nil)
	wantError(t, resp, body, http.StatusNotFound, platform.CodeExecutionNotFound)
	resp, body = h.do(http.MethodPost, "/executions/"+id+"/cancel", nil, platform.HeaderTenantID, "t2")
	wantError(t, resp, body, http.StatusNotFound, platform.CodeExecutionNotFound)
}

func TestCancelFinishedIsUnchanged(t *testing.T) {
	h := newHarness(t, mock.Options{})
	resp, body := h.create("k1", execBody("run1"), platform.HeaderPrefer, "wait=5")
	wantStatus(t, resp, body, http.StatusOK)
	id := decode[platform.Execution](t, body).ExecutionID
	resp, body = h.do(http.MethodPost, "/executions/"+id+"/cancel", nil)
	wantStatus(t, resp, body, http.StatusAccepted)
	if x := decode[platform.Execution](t, body); x.Status != platform.ExecutionSucceeded {
		t.Errorf("status %s, want succeeded", x.Status)
	}
}

func TestIdempotency(t *testing.T) {
	h := newHarness(t, mock.Options{Latency: 100 * time.Millisecond})
	body := `{"agent_id":"alert-triage","agent_version":3,"input":{"alert":{"id":"a1"}},"context":{"workflow_id":"wf@1","run_id":"r1","node_path":"n","attempt":1},"timeout_ms":60000}`
	reordered := `{ "timeout_ms": 60000.0, "context": {"attempt": 1, "node_path": "n", "run_id": "r1", "workflow_id": "wf@1"},
		"input": {"alert": {"id": "a1"}}, "agent_version": 3, "agent_id": "alert-triage" }`

	resp, data := h.create("k1", body)
	wantStatus(t, resp, data, http.StatusAccepted)
	first := decode[platform.Execution](t, data)

	// A redelivered job sends the same key and the same JSON value, and gets the same execution.
	resp, data = h.create("k1", reordered, platform.HeaderPrefer, "wait=5")
	wantStatus(t, resp, data, http.StatusOK)
	if x := decode[platform.Execution](t, data); x.ExecutionID != first.ExecutionID || x.Status != platform.ExecutionSucceeded {
		t.Fatalf("replay returned %s", data)
	}
	execs := h.srv.Executions()
	if len(execs) != 1 || execs[0].Replays != 1 || execs[0].IdempotencyKey != "k1" {
		t.Fatalf("executions %+v", execs)
	}
	if calls := h.srv.Calls(mock.OpCreateExecution); len(calls) != 2 {
		t.Errorf("%d createExecution calls, want 2", len(calls))
	}

	changed := strings.Replace(body, `"a1"`, `"a2"`, 1)
	resp, data = h.create("k1", changed)
	if e := wantError(t, resp, data, http.StatusConflict, platform.CodeIdempotencyKeyReused); e.Retryable {
		t.Error("IDEMPOTENCY_KEY_REUSED retryable")
	}

	// Keys are scoped to the tenant.
	resp, data = h.create("k1", body, platform.HeaderTenantID, "t2")
	wantStatus(t, resp, data, http.StatusAccepted)
	if n := len(h.srv.Executions()); n != 2 {
		t.Errorf("%d executions, want 2", n)
	}
}

func TestReplayAfterDisable(t *testing.T) {
	h := newHarness(t, mock.Options{})
	resp, body := h.create("k1", execBody("run1"), platform.HeaderPrefer, "wait=5")
	wantStatus(t, resp, body, http.StatusOK)
	id := decode[platform.Execution](t, body).ExecutionID

	if err := h.srv.SetVersionStatus("alert-triage", 3, platform.VersionDisabled); err != nil {
		t.Fatal(err)
	}
	resp, body = h.create("k1", execBody("run1"))
	wantStatus(t, resp, body, http.StatusOK)
	if got := decode[platform.Execution](t, body).ExecutionID; got != id {
		t.Errorf("replay returned %s, want %s", got, id)
	}
	resp, body = h.create("k2", execBody("run1"))
	wantError(t, resp, body, http.StatusConflict, platform.CodeAgentVersionDisabled)
}

func TestFinishHang(t *testing.T) {
	h := newHarness(t, mock.Options{})
	h.srv.Script(mock.Match{}, mock.Hang())
	resp, body := h.create("k1", execBody("run1"))
	wantStatus(t, resp, body, http.StatusAccepted)
	id := decode[platform.Execution](t, body).ExecutionID

	if err := h.srv.Finish(id, mock.Succeed(json.RawMessage(`null`))); err != nil {
		t.Fatal(err)
	}
	x := h.poll(id)
	if x.Status != platform.ExecutionSucceeded || string(x.Output) != "null" {
		t.Errorf("execution %+v output %q", x, x.Output)
	}
	if err := h.srv.Finish(id, mock.Succeed(nil)); err == nil {
		t.Error("Finish on a terminal execution succeeded")
	}
	if err := h.srv.Finish(id, mock.Hang()); err == nil {
		t.Error("Finish with Hang succeeded")
	}
}

func TestScriptMatching(t *testing.T) {
	h := newHarness(t, mock.Options{})
	h.srv.Script(mock.Match{NodePath: "other"}, mock.Fail(platform.CodeInternal))
	h.srv.Script(mock.Match{AgentID: "alert-triage", Attempt: 1},
		mock.Fail(platform.CodeProviderUnavailable),
		mock.Fail(platform.CodeTimeout),
	)
	codes := []platform.ErrorCode{platform.CodeProviderUnavailable, platform.CodeTimeout, ""}
	for i, want := range codes {
		resp, body := h.create("k"+string(rune('1'+i)), execBody("run1"), platform.HeaderPrefer, "wait=5")
		wantStatus(t, resp, body, http.StatusOK)
		x := decode[platform.Execution](t, body)
		var got platform.ErrorCode
		if x.Error != nil {
			got = x.Error.Code
		}
		if got != want {
			t.Errorf("execution %d: code %q, want %q", i, got, want)
		}
	}
}

func version(id string, n int, status platform.AgentVersionStatus, tags ...string) mock.AgentVersion {
	v := mock.AlertTriage()
	v.AgentID, v.Version, v.Status, v.Tags = id, n, status, tags
	v.Name = strings.ToUpper(id[:1]) + id[1:]
	return v
}

func TestCatalog(t *testing.T) {
	h := newHarness(t, mock.Options{})
	for _, v := range []mock.AgentVersion{
		version("phish-summarizer", 1, platform.VersionPublished, "email"),
		version("phish-summarizer", 2, platform.VersionDeprecated, "email"),
		version("legacy", 1, platform.VersionDeprecated),
		version("private", 1, platform.VersionPublished),
	} {
		if err := h.srv.AddAgentVersion(v); err != nil {
			t.Fatal(err)
		}
	}
	if err := h.srv.RestrictAgent("private", "t2"); err != nil {
		t.Fatal(err)
	}

	list := func(query string, headers ...string) platform.AgentList {
		t.Helper()
		resp, body := h.do(http.MethodGet, "/agents"+query, nil, headers...)
		wantStatus(t, resp, body, http.StatusOK)
		return decode[platform.AgentList](t, body)
	}
	ids := func(l platform.AgentList) string {
		var out []string
		for _, a := range l.Items {
			out = append(out, a.AgentID)
		}
		return strings.Join(out, ",")
	}

	all := list("")
	if got := ids(all); got != "alert-triage,phish-summarizer" {
		t.Errorf("default list %s", got)
	}
	if l := all.Items[1].LatestVersion; l == nil || *l != 1 {
		t.Errorf("phish-summarizer latest_version %v, want 1", l)
	}
	dep := list("?status=deprecated")
	if got := ids(dep); got != "legacy,phish-summarizer" {
		t.Errorf("deprecated list %s", got)
	}
	if dep.Items[0].LatestVersion != nil {
		t.Errorf("legacy latest_version %d, want null", *dep.Items[0].LatestVersion)
	}
	if got := ids(list("?search=PHISH")); got != "phish-summarizer" {
		t.Errorf("search %s", got)
	}
	if got := ids(list("?tags=security,triage")); got != "alert-triage" {
		t.Errorf("tags %s", got)
	}
	if got := ids(list("?tags=security,email")); got != "" {
		t.Errorf("tags with no match %s", got)
	}
	if got := ids(list("", platform.HeaderTenantID, "t2")); got != "alert-triage,phish-summarizer,private" {
		t.Errorf("t2 list %s", got)
	}

	page1 := list("?limit=1")
	if ids(page1) != "alert-triage" || page1.NextCursor == nil {
		t.Fatalf("page 1 %+v", page1)
	}
	page2 := list("?limit=1&cursor=" + *page1.NextCursor)
	if ids(page2) != "phish-summarizer" || page2.NextCursor != nil {
		t.Errorf("page 2 %+v", page2)
	}
	for _, q := range []string{"?limit=0", "?limit=201", "?cursor=x", "?status=disabled"} {
		resp, body := h.do(http.MethodGet, "/agents"+q, nil)
		wantError(t, resp, body, http.StatusBadRequest, platform.CodeRequestInvalid)
	}

	resp, body := h.do(http.MethodGet, "/agents/alert-triage", nil)
	wantStatus(t, resp, body, http.StatusOK)
	if a := decode[platform.AgentSummary](t, body); a.Name != "Alert triage" || a.LatestVersion == nil || *a.LatestVersion != 3 {
		t.Errorf("agent %s", body)
	}
	resp, body = h.do(http.MethodGet, "/agents/nope", nil)
	wantError(t, resp, body, http.StatusNotFound, platform.CodeAgentNotFound)
	resp, body = h.do(http.MethodGet, "/agents/private", nil)
	wantError(t, resp, body, http.StatusNotFound, platform.CodeAgentNotFound)

	resp, body = h.do(http.MethodGet, "/agents/phish-summarizer/versions", nil)
	wantStatus(t, resp, body, http.StatusOK)
	vs := decode[platform.AgentVersionList](t, body)
	if len(vs.Items) != 2 || vs.Items[0].Version != 2 || vs.Items[0].Status != platform.VersionDeprecated {
		t.Errorf("versions %s", body)
	}
	resp, body = h.do(http.MethodGet, "/agents/nope/versions", nil)
	wantError(t, resp, body, http.StatusNotFound, platform.CodeAgentNotFound)

	resp, body = h.do(http.MethodGet, "/agents/alert-triage/versions/3", nil)
	wantStatus(t, resp, body, http.StatusOK)
	var served map[string]json.RawMessage
	if err := json.Unmarshal(body, &served); err != nil {
		t.Fatal(err)
	}
	digest, err := platform.ContractDigest(served["input_schema"], served["output_schema"], served["limits"])
	if err != nil {
		t.Fatal(err)
	}
	if got := decode[platform.AgentVersionContract](t, body).Digest; got != digest {
		t.Errorf("served digest %s, recomputed %s", got, digest)
	}
	resp, body = h.do(http.MethodGet, "/agents/alert-triage/versions/abc", nil)
	wantError(t, resp, body, http.StatusBadRequest, platform.CodeRequestInvalid)
	resp, body = h.do(http.MethodGet, "/agents/alert-triage/versions/9", nil)
	wantError(t, resp, body, http.StatusNotFound, platform.CodeAgentVersionNotFound)
}

func TestAuthAndTenant(t *testing.T) {
	h := newHarness(t, mock.Options{Token: "s3cret", Tenants: []string{"t1", "t2"}})
	auth := "Bearer s3cret"

	resp, body := h.do(http.MethodGet, "/agents", nil)
	wantError(t, resp, body, http.StatusUnauthorized, platform.CodeUnauthorized)
	resp, body = h.do(http.MethodGet, "/agents", nil, "Authorization", "Bearer wrong")
	wantError(t, resp, body, http.StatusUnauthorized, platform.CodeUnauthorized)
	resp, body = h.do(http.MethodGet, "/agents", nil, "Authorization", auth, platform.HeaderTenantID, "")
	wantError(t, resp, body, http.StatusBadRequest, platform.CodeRequestInvalid)
	resp, body = h.do(http.MethodGet, "/agents", nil, "Authorization", auth, platform.HeaderTenantID, "t9")
	wantError(t, resp, body, http.StatusForbidden, platform.CodeForbidden)
	resp, body = h.do(http.MethodGet, "/agents", nil, "Authorization", auth)
	wantStatus(t, resp, body, http.StatusOK)
	if resp.Header.Get(platform.HeaderRateLimitLimit) == "" {
		t.Error("missing rate-limit headers")
	}
}

func TestRateLimit(t *testing.T) {
	h := newHarness(t, mock.Options{RateLimit: 2})
	for i, want := range []string{"1", "0"} {
		resp, body := h.do(http.MethodGet, "/agents", nil)
		wantStatus(t, resp, body, http.StatusOK)
		if got := resp.Header.Get(platform.HeaderRateLimitRemaining); got != want {
			t.Errorf("request %d: RateLimit-Remaining %s, want %s", i, got, want)
		}
	}
	resp, body := h.do(http.MethodGet, "/agents", nil)
	e := wantError(t, resp, body, http.StatusTooManyRequests, platform.CodeQuotaExceeded)
	if !e.Retryable || e.RetryAfterMs == nil || resp.Header.Get(platform.HeaderRetryAfter) == "" {
		t.Errorf("rate-limited response %s, Retry-After %q", body, resp.Header.Get(platform.HeaderRetryAfter))
	}
	if got := resp.Header.Get(platform.HeaderRateLimitLimit); got != "2" {
		t.Errorf("RateLimit-Limit %s", got)
	}
}

func TestFailRequests(t *testing.T) {
	h := newHarness(t, mock.Options{})
	resp, body := h.create("k1", execBody("run1"), platform.HeaderPrefer, "wait=5")
	wantStatus(t, resp, body, http.StatusOK)
	id := decode[platform.Execution](t, body).ExecutionID

	h.srv.FailRequests(mock.OpGetExecution, mock.Reject(platform.CodeProviderUnavailable))
	resp, body = h.do(http.MethodGet, "/executions/"+id, nil)
	if e := wantError(t, resp, body, http.StatusServiceUnavailable, platform.CodeProviderUnavailable); !e.Retryable {
		t.Error("not retryable")
	}
	if got := resp.Header.Get(platform.HeaderRetryAfter); got != "1" {
		t.Errorf("Retry-After %q, want 1", got)
	}
	resp, body = h.do(http.MethodGet, "/executions/"+id, nil)
	wantStatus(t, resp, body, http.StatusOK)

	calls := h.srv.Calls(mock.OpGetExecution)
	if len(calls) != 2 || calls[0].Status != 503 || calls[1].Status != 200 || calls[0].TenantID != "t1" {
		t.Errorf("calls %+v", calls)
	}
}

func TestBasePath(t *testing.T) {
	h := newHarness(t, mock.Options{BasePath: "/v1", Latency: 100 * time.Millisecond})
	if !strings.HasSuffix(h.srv.URL, "/v1") {
		t.Fatalf("URL %s", h.srv.URL)
	}
	resp, body := h.create("k1", execBody("run1"))
	wantStatus(t, resp, body, http.StatusAccepted)
	id := decode[platform.Execution](t, body).ExecutionID
	if got := resp.Header.Get("Location"); got != "/v1/executions/"+id {
		t.Errorf("Location %s", got)
	}
	h.poll(id)
}

func TestPreferParsing(t *testing.T) {
	tests := []struct {
		prefer, applied string
		status          int
	}{
		{"respond-async, wait=2", "wait=2", http.StatusOK},
		{"WAIT=2", "wait=2", http.StatusOK},
		{`wait="2"; x=y`, "wait=2", http.StatusOK},
		{"wait=30", "wait=3", http.StatusOK}, // capped by MaxWait
		{"wait=abc", "", http.StatusAccepted},
		{"respond-async", "", http.StatusAccepted},
	}
	for _, tt := range tests {
		t.Run(tt.prefer, func(t *testing.T) {
			h := newHarness(t, mock.Options{Latency: 50 * time.Millisecond, MaxWait: 3 * time.Second})
			resp, body := h.create("k1", execBody("run1"), platform.HeaderPrefer, tt.prefer)
			wantStatus(t, resp, body, tt.status)
			if got := resp.Header.Get(platform.HeaderPreferenceApplied); got != tt.applied {
				t.Errorf("Preference-Applied %q, want %q", got, tt.applied)
			}
		})
	}
}

func TestTrace(t *testing.T) {
	h := newHarness(t, mock.Options{})
	resp, body := h.create("k1", execBody("run1"), platform.HeaderPrefer, "wait=5")
	wantStatus(t, resp, body, http.StatusOK)
	id := decode[platform.Execution](t, body).ExecutionID
	resp, body = h.do(http.MethodGet, "/executions/"+id+"/trace", nil)
	wantStatus(t, resp, body, http.StatusOK)
	tr := decode[platform.ExecutionTrace](t, body)
	if tr.ExecutionID != id || len(tr.Steps) != 1 || tr.Steps[0].EndedAt == nil {
		t.Errorf("trace %s", body)
	}
	resp, body = h.do(http.MethodGet, "/executions/nope/trace", nil)
	wantError(t, resp, body, http.StatusNotFound, platform.CodeExecutionNotFound)
}

func TestAddAgentVersionRejectsBadContracts(t *testing.T) {
	srv := mock.New(mock.Options{})
	defer srv.Close()
	bad := []func(*mock.AgentVersion){
		func(v *mock.AgentVersion) { v.AgentID = "" },
		func(v *mock.AgentVersion) { v.Version = 0 },
		func(v *mock.AgentVersion) { v.InputSchema = json.RawMessage(`[]`) },
		func(v *mock.AgentVersion) { v.Limits.MaxTimeoutMs = 1000 },
		func(v *mock.AgentVersion) { v.Digest = "sha256:" + strings.Repeat("0", 64) },
	}
	for i, f := range bad {
		v := mock.AlertTriage()
		f(&v)
		if err := srv.AddAgentVersion(v); err == nil {
			t.Errorf("case %d: AddAgentVersion succeeded", i)
		}
	}
}

func TestOutcomeConstructorsEnforceChannels(t *testing.T) {
	mustPanic := func(name string, f func()) {
		t.Helper()
		defer func() {
			if recover() == nil {
				t.Errorf("%s did not panic", name)
			}
		}()
		f()
	}
	mustPanic("Fail(INPUT_SCHEMA_INVALID)", func() { mock.Fail(platform.CodeInputSchemaInvalid) })
	mustPanic("Reject(TIMEOUT)", func() { mock.Reject(platform.CodeTimeout) })
	mustPanic("FailRequests(Fail)", func() {
		mock.New(mock.Options{}).FailRequests(mock.OpGetAgent, mock.Fail(platform.CodeTimeout))
	})
}
