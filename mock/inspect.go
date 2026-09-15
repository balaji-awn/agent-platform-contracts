package mock

import (
	"fmt"
	"slices"
	"time"

	"github.com/sbalaji6/agent-platform-contracts/platform"
)

// Call is one request the mock received.
type Call struct {
	Operation      Operation
	Method         string
	Path           string
	TenantID       string
	IdempotencyKey string
	Status         int
	At             time.Time
}

// Calls returns the requests received so far, oldest first, limited to ops if any are given.
func (s *Server) Calls(ops ...Operation) []Call {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []Call
	for _, c := range s.calls {
		if len(ops) == 0 || slices.Contains(ops, c.Operation) {
			out = append(out, c)
		}
	}
	return out
}

// ExecutionRecord is an execution with the details a test may assert on.
type ExecutionRecord struct {
	platform.Execution
	TenantID string
	// IdempotencyKey is the key the execution was created with.
	IdempotencyKey string
	// Request is the request that created the execution.
	Request platform.ExecutionRequest
	// Replays counts idempotent replays served for this execution.
	Replays int
	// CancelCalls counts cancelExecution calls for this execution.
	CancelCalls int
}

// Executions returns every execution created so far, oldest first. Idempotent replays do not
// create executions, so a key sent twice appears once.
func (s *Server) Executions() []ExecutionRecord {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]ExecutionRecord, 0, len(s.order))
	for _, e := range s.order {
		out = append(out, e.record())
	}
	return out
}

// Execution returns the execution with the given ID.
func (s *Server) Execution(id string) (ExecutionRecord, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	e := s.executions[id]
	if e == nil {
		return ExecutionRecord{}, false
	}
	return e.record(), true
}

func (e *execution) record() ExecutionRecord {
	return ExecutionRecord{
		Execution:      e.snapshot(),
		TenantID:       e.tenant,
		IdempotencyKey: e.key,
		Request:        e.req,
		Replays:        e.replays,
		CancelCalls:    e.cancelCalls,
	}
}

// Finish applies o to a running execution now, ignoring o's delay. o must be built with Succeed or
// Fail. Use it to end a Hang execution at a moment the test chooses.
func (s *Server) Finish(executionID string, o Outcome) error {
	if o.kind != kindSucceed && o.kind != kindFail {
		return fmt.Errorf("mock: Finish takes a Succeed or Fail outcome")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	e := s.executions[executionID]
	if e == nil {
		return fmt.Errorf("mock: no execution %s", executionID)
	}
	if e.x.Status.Terminal() {
		return fmt.Errorf("mock: execution %s is already %s", executionID, e.x.Status)
	}
	s.finishLocked(e, o)
	return nil
}
