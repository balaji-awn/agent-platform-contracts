package mock

import (
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"net/http/httptest"
	"slices"
	"strconv"
	"sync"
	"time"

	"github.com/sbalaji6/agent-platform-contracts/platform"
)

// Options configures a Server. The zero value is usable.
type Options struct {
	// Token, if set, is the bearer token every request must carry; others get 401 UNAUTHORIZED.
	// Empty accepts any request.
	Token string
	// Tenants, if set, lists the tenant IDs allowed to call the API; others get 403 FORBIDDEN.
	Tenants []string
	// BasePath is a prefix the API is served under, such as "/v1".
	BasePath string
	// Latency is how long an execution runs when its outcome sets no delay. Zero finishes
	// executions immediately.
	Latency time.Duration
	// MaxWait caps the Prefer: wait window. It is the sync-vs-202 cutoff: an execution that runs
	// longer than min(requested wait, MaxWait) gets a 202. Default 60s.
	MaxWait time.Duration
	// TimeoutScale multiplies timeout_ms before the mock enforces it, so tests can hit TIMEOUT
	// without waiting a full second (timeout_ms is at least 1000). Default 1.
	TimeoutScale float64
	// RateLimit is the number of requests per RateLimitWindow advertised in the RateLimit-* headers.
	// Requests beyond it get 429 QUOTA_EXCEEDED. Default 10000.
	RateLimit int
	// RateLimitWindow is the length of the fixed rate-limit window. Default 1m.
	RateLimitWindow time.Duration
	// Validator checks inputs against input schemas. Default BasicValidator{}.
	Validator InputValidator
}

// Operation names an API operation by its operationId in api/openapi.yaml.
type Operation string

const (
	OpListAgents        Operation = "listAgents"
	OpGetAgent          Operation = "getAgent"
	OpListAgentVersions Operation = "listAgentVersions"
	OpGetAgentVersion   Operation = "getAgentVersion"
	OpCreateExecution   Operation = "createExecution"
	OpGetExecution      Operation = "getExecution"
	OpCancelExecution   Operation = "cancelExecution"
	OpGetExecutionTrace Operation = "getExecutionTrace"
)

type route struct {
	op      Operation
	method  string
	path    string
	handler func(*Server, http.ResponseWriter, *http.Request, string)
}

// routes mirrors the paths of api/openapi.yaml. spec_test.go checks that they match.
var routes = []route{
	{OpListAgents, http.MethodGet, "/agents", (*Server).listAgents},
	{OpGetAgent, http.MethodGet, "/agents/{agent_id}", (*Server).getAgent},
	{OpListAgentVersions, http.MethodGet, "/agents/{agent_id}/versions", (*Server).listAgentVersions},
	{OpGetAgentVersion, http.MethodGet, "/agents/{agent_id}/versions/{version}", (*Server).getAgentVersion},
	{OpCreateExecution, http.MethodPost, "/executions", (*Server).createExecution},
	{OpGetExecution, http.MethodGet, "/executions/{execution_id}", (*Server).getExecution},
	{OpCancelExecution, http.MethodPost, "/executions/{execution_id}/cancel", (*Server).cancelExecution},
	{OpGetExecutionTrace, http.MethodGet, "/executions/{execution_id}/trace", (*Server).getExecutionTrace},
}

// Server is an in-memory agent platform. It implements http.Handler; use NewTestServer to serve it
// on a local port for the duration of a test.
type Server struct {
	// URL is the base URL, including BasePath, when the server was started by NewTestServer.
	URL string

	opts    Options
	mux     *http.ServeMux
	closing chan struct{}
	hs      *httptest.Server

	mu          sync.Mutex
	closed      bool
	agents      map[string]*agent
	executions  map[string]*execution
	order       []*execution
	byKey       map[idemKey]*execution
	scripts     []*script
	faults      map[Operation][]Outcome
	calls       []Call
	seq         int
	windowEnd   time.Time
	windowCount int
}

// New returns a Server with an empty catalog.
func New(opts Options) *Server {
	if opts.MaxWait <= 0 {
		opts.MaxWait = 60 * time.Second
	}
	if opts.TimeoutScale <= 0 {
		opts.TimeoutScale = 1
	}
	if opts.RateLimit <= 0 {
		opts.RateLimit = 10000
	}
	if opts.RateLimitWindow <= 0 {
		opts.RateLimitWindow = time.Minute
	}
	if opts.Validator == nil {
		opts.Validator = BasicValidator{}
	}
	s := &Server{
		opts:       opts,
		mux:        http.NewServeMux(),
		closing:    make(chan struct{}),
		agents:     map[string]*agent{},
		executions: map[string]*execution{},
		byKey:      map[idemKey]*execution{},
		faults:     map[Operation][]Outcome{},
	}
	for _, rt := range routes {
		rt := rt
		s.mux.HandleFunc(rt.method+" "+opts.BasePath+rt.path, func(w http.ResponseWriter, r *http.Request) {
			s.serve(rt, w, r)
		})
	}
	return s
}

// TB is the part of testing.TB that NewTestServer uses.
type TB interface {
	Helper()
	Cleanup(func())
}

// NewTestServer starts a Server on a local httptest server, sets its URL, and closes it when the
// test finishes.
func NewTestServer(t TB, opts Options) *Server {
	t.Helper()
	s := New(opts)
	s.hs = httptest.NewServer(s)
	s.URL = s.hs.URL + opts.BasePath
	t.Cleanup(s.Close)
	return s
}

// ServeHTTP serves the platform API.
func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	s.mux.ServeHTTP(w, r)
}

// Close stops pending executions, releases requests waiting on Prefer: wait, and stops the
// httptest server if NewTestServer started one. It is safe to call more than once.
func (s *Server) Close() {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return
	}
	s.closed = true
	close(s.closing)
	for _, e := range s.order {
		e.stopTimers()
	}
	s.mu.Unlock()
	if s.hs != nil {
		s.hs.Close()
	}
}

// statusRecorder captures the status code of a response for the call log.
type statusRecorder struct {
	http.ResponseWriter
	status int
}

func (r *statusRecorder) WriteHeader(code int) {
	r.status = code
	r.ResponseWriter.WriteHeader(code)
}

func (s *Server) serve(rt route, w http.ResponseWriter, r *http.Request) {
	rec := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
	tenant := r.Header.Get(platform.HeaderTenantID)
	defer func() {
		s.mu.Lock()
		s.calls = append(s.calls, Call{
			Operation:      rt.op,
			Method:         r.Method,
			Path:           r.URL.Path,
			TenantID:       tenant,
			IdempotencyKey: r.Header.Get(platform.HeaderIdempotencyKey),
			Status:         rec.status,
			At:             time.Now(),
		})
		s.mu.Unlock()
	}()

	if s.opts.Token != "" && r.Header.Get("Authorization") != "Bearer "+s.opts.Token {
		s.rateLimitHeaders(rec)
		writeError(rec, errorFor(platform.CodeUnauthorized, "missing or invalid bearer token"))
		return
	}
	if tenant == "" {
		s.rateLimitHeaders(rec)
		writeError(rec, errorFor(platform.CodeRequestInvalid, "missing "+platform.HeaderTenantID+" header"))
		return
	}
	if len(s.opts.Tenants) > 0 && !slices.Contains(s.opts.Tenants, tenant) {
		s.rateLimitHeaders(rec)
		writeError(rec, errorFor(platform.CodeForbidden, "tenant "+tenant+" may not call this API"))
		return
	}
	if limited := s.rateLimitHeaders(rec); limited != nil {
		writeError(rec, *limited)
		return
	}
	if o, ok := s.popFault(rt.op); ok {
		if !s.sleep(r, o.delay) {
			return
		}
		writeError(rec, o.rejection())
		return
	}
	rt.handler(s, rec, r, tenant)
}

// rateLimitHeaders counts the request against the fixed window and sets the RateLimit-* headers.
// It returns the rejection to send when the request is over the limit.
func (s *Server) rateLimitHeaders(w http.ResponseWriter) *rejection {
	s.mu.Lock()
	now := time.Now()
	if !now.Before(s.windowEnd) {
		s.windowEnd = now.Add(s.opts.RateLimitWindow)
		s.windowCount = 0
	}
	s.windowCount++
	remaining := max(s.opts.RateLimit-s.windowCount, 0)
	reset := s.windowEnd.Sub(now)
	over := s.windowCount > s.opts.RateLimit
	s.mu.Unlock()

	h := w.Header()
	h.Set(platform.HeaderRateLimitLimit, strconv.Itoa(s.opts.RateLimit))
	h.Set(platform.HeaderRateLimitRemaining, strconv.Itoa(remaining))
	h.Set(platform.HeaderRateLimitReset, strconv.Itoa(ceilSeconds(reset)))
	if !over {
		return nil
	}
	rej := errorFor(platform.CodeQuotaExceeded, "request rate limit exceeded")
	ms := reset.Milliseconds()
	rej.body.RetryAfterMs = &ms
	return &rej
}

// sleep waits d unless the request or the server ends first. It reports whether d elapsed.
func (s *Server) sleep(r *http.Request, d time.Duration) bool {
	if d <= 0 {
		return true
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-t.C:
		return true
	case <-r.Context().Done():
		return false
	case <-s.closing:
		return false
	}
}

// rejection is a request-level error response.
type rejection struct {
	status int
	body   platform.Error
}

func errorFor(code platform.ErrorCode, msg string, details ...platform.FieldError) rejection {
	info, ok := code.Info()
	status := info.HTTPStatus
	if !ok || status == 0 {
		status = http.StatusInternalServerError
	}
	return rejection{status: status, body: platform.Error{
		Code:      code,
		Message:   msg,
		Retryable: info.Retryable,
		Details:   details,
	}}
}

func writeError(w http.ResponseWriter, rej rejection) {
	if rej.body.RetryAfterMs != nil {
		w.Header().Set(platform.HeaderRetryAfter, strconv.Itoa(ceilSeconds(time.Duration(*rej.body.RetryAfterMs)*time.Millisecond)))
	}
	writeJSON(w, rej.status, platform.ErrorEnvelope{Error: rej.body})
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	body, err := json.Marshal(v)
	if err != nil {
		status = http.StatusInternalServerError
		body = []byte(fmt.Sprintf(`{"error":{"code":"INTERNAL","message":%q,"retryable":true}}`, err.Error()))
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	w.Write(body)
}

func ceilSeconds(d time.Duration) int {
	if d <= 0 {
		return 0
	}
	return int(math.Ceil(d.Seconds()))
}
