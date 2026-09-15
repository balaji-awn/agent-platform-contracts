package mock

import (
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"github.com/sbalaji6/agent-platform-contracts/platform"
)

type outcomeKind int

const (
	kindSucceed outcomeKind = iota
	kindFail
	kindReject
	kindHang
	kindCancel
)

// Outcome is what the mock does with one execution or request. Build one with Succeed, Fail,
// Reject, or Hang, and refine it with its methods, which return a modified copy.
type Outcome struct {
	kind          outcomeKind
	delay         time.Duration
	hasDelay      bool
	output        json.RawMessage
	err           platform.Error
	status        int
	retryAfter    time.Duration
	ignoreTimeout bool
}

// Succeed completes the execution with output. A nil output uses the version's DefaultOutput; pass
// json.RawMessage("null") for a null output. Output is marshaled with encoding/json, and a
// json.RawMessage is used as is. It panics if output cannot be marshaled.
func Succeed(output any) Outcome {
	o := Outcome{kind: kindSucceed}
	switch v := output.(type) {
	case nil:
	case json.RawMessage:
		if !json.Valid(v) {
			panic("mock: Succeed: output is not valid JSON")
		}
		o.output = v
	default:
		raw, err := json.Marshal(v)
		if err != nil {
			panic(fmt.Sprintf("mock: Succeed: %v", err))
		}
		o.output = raw
	}
	return o
}

// Fail ends the execution with status failed and an error with code, which is an execution-level
// failure (a 200 carrying the error). Retryable defaults from the spec's table.
// It panics if the spec defines code as request-level only; use Reject for those.
func Fail(code platform.ErrorCode) Outcome {
	info, known := code.Info()
	if known && !info.ExecutionLevel {
		panic(fmt.Sprintf("mock: %s is never an execution-level failure; use Reject", code))
	}
	return Outcome{kind: kindFail, err: defaultError(code, info)}
}

// Reject refuses the request before an execution exists, with the HTTP status the spec gives code
// (500 for unknown codes; override with WithHTTPStatus). No execution is created and the
// idempotency key is not recorded. It panics if the spec defines code as execution-level only; use
// Fail for those.
func Reject(code platform.ErrorCode) Outcome {
	info, known := code.Info()
	if known && info.HTTPStatus == 0 {
		panic(fmt.Sprintf("mock: %s is only an execution-level failure; use Fail", code))
	}
	status := info.HTTPStatus
	if !known {
		status = http.StatusInternalServerError
	}
	o := Outcome{kind: kindReject, err: defaultError(code, info), status: status}
	if info.Retryable && (status == http.StatusTooManyRequests || status == http.StatusServiceUnavailable) {
		o.retryAfter = time.Second
	}
	return o
}

// Hang keeps the execution running until it is cancelled, finished with Server.Finish, or its
// timeout_ms elapses (see IgnoreTimeout).
func Hang() Outcome {
	return Outcome{kind: kindHang}
}

func defaultError(code platform.ErrorCode, info platform.CodeInfo) platform.Error {
	return platform.Error{Code: code, Message: "mock: " + string(code), Retryable: info.Retryable}
}

// After sets how long the execution runs before the outcome applies, overriding Options.Latency.
// For Reject it delays the response. It has no effect on Hang.
func (o Outcome) After(d time.Duration) Outcome {
	o.delay, o.hasDelay = d, true
	return o
}

// WithMessage sets the error message of a Fail or Reject outcome.
func (o Outcome) WithMessage(msg string) Outcome {
	o.err.Message = msg
	return o
}

// WithRetryable overrides the retryable flag of a Fail or Reject outcome.
func (o Outcome) WithRetryable(retryable bool) Outcome {
	o.err.Retryable = retryable
	return o
}

// WithRetryAfter sets the Retry-After header of a Reject outcome, rounded up to whole seconds; zero
// sends none. Retryable 429 and 503 rejections default to 1s. Other outcomes ignore it, since failed
// executions carry no wait.
func (o Outcome) WithRetryAfter(d time.Duration) Outcome {
	o.retryAfter = d
	return o
}

// WithHTTPStatus overrides the status of a Reject outcome.
func (o Outcome) WithHTTPStatus(status int) Outcome {
	o.status = status
	return o
}

// IgnoreTimeout keeps the mock from failing the execution with TIMEOUT when timeout_ms elapses, to
// simulate a platform that overruns so the caller's own deadline and cancel path can be tested.
func (o Outcome) IgnoreTimeout() Outcome {
	o.ignoreTimeout = true
	return o
}

func (o Outcome) rejection() rejection {
	return rejection{status: o.status, body: o.err, retryAfter: o.retryAfter}
}

// Match selects the executions a script applies to. Zero fields match anything.
type Match struct {
	TenantID       string
	AgentID        string
	Version        int
	RunID          string
	NodePath       string
	Attempt        int
	IdempotencyKey string
}

func (m Match) matches(tenant, key string, req *platform.ExecutionRequest) bool {
	switch {
	case m.TenantID != "" && m.TenantID != tenant,
		m.AgentID != "" && m.AgentID != req.AgentID,
		m.Version != 0 && m.Version != req.AgentVersion,
		m.RunID != "" && m.RunID != req.Context.RunID,
		m.NodePath != "" && m.NodePath != req.Context.NodePath,
		m.Attempt != 0 && m.Attempt != req.Context.Attempt,
		m.IdempotencyKey != "" && m.IdempotencyKey != key:
		return false
	}
	return true
}

type script struct {
	match    Match
	outcomes []Outcome
	next     int
}

// Script queues outcomes for new executions that match m. Each new execution takes the next
// outcome of the first script, in registration order, that matches it and has outcomes left.
// Idempotent replays and requests rejected by validation take nothing. Executions that no script
// matches succeed with the version's DefaultOutput after Options.Latency.
func (s *Server) Script(m Match, outcomes ...Outcome) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.scripts = append(s.scripts, &script{match: m, outcomes: append([]Outcome(nil), outcomes...)})
}

func (s *Server) nextOutcomeLocked(tenant, key string, req *platform.ExecutionRequest) Outcome {
	for _, sc := range s.scripts {
		if sc.next < len(sc.outcomes) && sc.match.matches(tenant, key, req) {
			o := sc.outcomes[sc.next]
			sc.next++
			return o
		}
	}
	return Outcome{kind: kindSucceed}
}

// FailRequests makes the next calls to op fail, one rejection per call, after auth and tenant
// checks and before anything else, for example a 503 while polling getExecution. It panics unless
// every outcome was built with Reject.
func (s *Server) FailRequests(op Operation, rejections ...Outcome) {
	for _, o := range rejections {
		if o.kind != kindReject {
			panic("mock: FailRequests takes only Reject outcomes")
		}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.faults[op] = append(s.faults[op], rejections...)
}

func (s *Server) popFault(op Operation) (Outcome, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	q := s.faults[op]
	if len(q) == 0 {
		return Outcome{}, false
	}
	s.faults[op] = q[1:]
	return q[0], true
}
