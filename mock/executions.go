package mock

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/sbalaji6/agent-platform-contracts/internal/jcs"
	"github.com/sbalaji6/agent-platform-contracts/platform"
)

const maxBodyBytes = 10 << 20

type idemKey struct{ tenant, key string }

type execution struct {
	x             platform.Execution
	tenant        string
	key           string // empty for test executions
	canon         []byte // canonical request body, for replay comparison
	req           platform.ExecutionRequest
	defaultOutput json.RawMessage
	done          chan struct{}
	timers        []*time.Timer
	replays       int
	cancelCalls   int
	started       time.Time // for the mock trace
	finished      time.Time // zero until terminal
}

func (e *execution) stopTimers() {
	for _, t := range e.timers {
		t.Stop()
	}
	e.timers = nil
}

// snapshot returns a copy of the execution that shares no mutable state. The caller holds s.mu.
func (e *execution) snapshot() platform.Execution {
	x := e.x
	if e.x.Error != nil {
		err := *e.x.Error
		x.Error = &err
	}
	return x
}

// readBody reads the request body and returns its RFC 8785 canonical form. Decoding the canonical
// form lets integer fields accept any number with a zero fraction, such as 60000.0, as JSON Schema
// 2020-12 does, and makes bodies byte-comparable for idempotent replay.
func readBody(r *http.Request) ([]byte, rejection, bool) {
	raw, err := io.ReadAll(io.LimitReader(r.Body, maxBodyBytes+1))
	if err != nil {
		return nil, errorFor(platform.CodeRequestInvalid, "reading body: "+err.Error()), false
	}
	if len(raw) > maxBodyBytes {
		return nil, errorFor(platform.CodeRequestInvalid, "body exceeds 10 MiB"), false
	}
	canon, err := jcs.Transform(raw)
	if err != nil {
		return nil, errorFor(platform.CodeRequestInvalid, "body is not valid JSON: "+err.Error()), false
	}
	return canon, rejection{}, true
}

// decodeRaw decodes a JSON object body into v after checking that each required top-level field is
// present. strict rejects unknown fields at any depth.
func decodeRaw(raw []byte, v any, strict bool, required ...string) (rejection, bool) {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil || fields == nil {
		return errorFor(platform.CodeRequestInvalid, "body must be a JSON object"), false
	}
	var details []FieldError
	for _, name := range required {
		if _, ok := fields[name]; !ok {
			details = append(details, FieldError{Path: "/" + name, Message: "is required"})
		}
	}
	if len(details) > 0 {
		return errorFor(platform.CodeRequestInvalid, "missing required fields", details...), false
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	if strict {
		dec.DisallowUnknownFields()
	}
	if err := dec.Decode(v); err != nil {
		return errorFor(platform.CodeRequestInvalid, "invalid body: "+err.Error()), false
	}
	return rejection{}, true
}

// checkExecutionRequest applies the ExecutionRequest constraints that decoding does not.
func checkExecutionRequest(raw []byte, req *platform.ExecutionRequest) (rejection, bool) {
	var top struct {
		Context map[string]json.RawMessage `json:"context"`
	}
	_ = json.Unmarshal(raw, &top)
	var details []FieldError
	if top.Context == nil {
		details = append(details, FieldError{Path: "/context", Message: "must be an object"})
	} else {
		for _, f := range []string{"workflow_id", "run_id", "node_path"} {
			if _, ok := top.Context[f]; !ok {
				details = append(details, FieldError{Path: "/context/" + f, Message: "is required"})
			}
		}
		if _, ok := top.Context["attempt"]; ok && req.Context.Attempt < 1 {
			details = append(details, FieldError{Path: "/context/attempt", Message: "must be at least 1"})
		}
	}
	if req.AgentVersion < 1 {
		details = append(details, FieldError{Path: "/agent_version", Message: "must be at least 1"})
	}
	if req.TimeoutMs < 1000 {
		details = append(details, FieldError{Path: "/timeout_ms", Message: "must be at least 1000"})
	}
	if len(details) > 0 {
		return errorFor(platform.CodeRequestInvalid, "invalid request body", details...), false
	}
	return rejection{}, true
}

// admitLocked runs the version, timeout, and input checks for createExecution. v is nil when the
// version does not exist or is invisible.
func (s *Server) admitLocked(v *AgentVersion, timeoutMs int64, input json.RawMessage) (rejection, bool) {
	if v == nil {
		return errorFor(platform.CodeAgentVersionNotFound, "agent version not found"), false
	}
	if v.Status == platform.VersionDisabled {
		return errorFor(platform.CodeAgentVersionDisabled, fmt.Sprintf("%s@%d is disabled", v.AgentID, v.Version)), false
	}
	if timeoutMs > v.Limits.MaxTimeoutMs {
		msg := fmt.Sprintf("%d exceeds max_timeout_ms %d", timeoutMs, v.Limits.MaxTimeoutMs)
		return errorFor(platform.CodeRequestInvalid, "invalid request body", FieldError{Path: "/timeout_ms", Message: msg}), false
	}
	errs, err := s.opts.Validator.ValidateInput(v.InputSchema, input)
	if err != nil {
		return errorFor(platform.CodeInternal, "validator: "+err.Error()), false
	}
	if len(errs) > 0 {
		return errorFor(platform.CodeInputSchemaInvalid, "input does not match the input schema", errs...), false
	}
	return rejection{}, true
}

func (s *Server) createExecution(w http.ResponseWriter, r *http.Request, tenant string) {
	key := r.Header.Get(platform.HeaderIdempotencyKey)
	if key == "" || len(key) > 512 {
		writeError(w, errorFor(platform.CodeRequestInvalid, platform.HeaderIdempotencyKey+" header must be 1 to 512 characters"))
		return
	}
	canon, rej, ok := readBody(r)
	if !ok {
		writeError(w, rej)
		return
	}
	var req platform.ExecutionRequest
	if rej, ok := decodeRaw(canon, &req, true, "agent_id", "agent_version", "input", "context", "timeout_ms"); !ok {
		writeError(w, rej)
		return
	}
	if rej, ok := checkExecutionRequest(canon, &req); !ok {
		writeError(w, rej)
		return
	}

	s.mu.Lock()
	if e := s.byKey[idemKey{tenant, key}]; e != nil {
		if !bytes.Equal(e.canon, canon) {
			s.mu.Unlock()
			writeError(w, errorFor(platform.CodeIdempotencyKeyReused, "idempotency key was used with a different body"))
			return
		}
		e.replays++
		s.respondExecutionLocked(w, e)
		return
	}
	v := s.visibleVersionLocked(tenant, req.AgentID, req.AgentVersion)
	if rej, ok := s.admitLocked(v, req.TimeoutMs, req.Input); !ok {
		s.mu.Unlock()
		writeError(w, rej)
		return
	}
	o := s.nextOutcomeLocked(tenant, key, &req)
	if o.kind == kindReject {
		s.mu.Unlock()
		if s.sleep(r, o.delay) {
			writeError(w, o.rejection())
		}
		return
	}
	e := s.startLocked(tenant, key, canon, req, v, o)
	s.respondExecutionLocked(w, e)
}

// respondExecutionLocked writes 200 if e is terminal, else 202 with Location. It never waits. The
// caller holds s.mu, which it releases.
func (s *Server) respondExecutionLocked(w http.ResponseWriter, e *execution) {
	x := e.snapshot()
	s.mu.Unlock()
	if x.Status.Terminal() {
		writeJSON(w, http.StatusOK, x)
		return
	}
	w.Header().Set("Location", s.opts.BasePath+"/executions/"+x.ExecutionID)
	writeJSON(w, http.StatusAccepted, x)
}

// startLocked creates an execution and schedules its outcome and timeout.
func (s *Server) startLocked(tenant, key string, canon []byte, req platform.ExecutionRequest, v *AgentVersion, o Outcome) *execution {
	s.seq++
	id := fmt.Sprintf("exec_%06d", s.seq)
	e := &execution{
		tenant:        tenant,
		key:           key,
		canon:         canon,
		req:           req,
		defaultOutput: v.DefaultOutput,
		done:          make(chan struct{}),
		started:       time.Now().UTC(),
	}
	e.x = platform.Execution{
		ExecutionID:  id,
		Status:       platform.ExecutionRunning,
		AgentID:      v.AgentID,
		AgentVersion: v.Version,
		TraceID:      "trace_" + id,
	}
	s.executions[id] = e
	s.order = append(s.order, e)
	if key != "" {
		s.byKey[idemKey{tenant, key}] = e
	}

	if o.kind != kindHang {
		d := s.opts.Latency
		if o.hasDelay {
			d = o.delay
		}
		if d <= 0 {
			s.finishLocked(e, o)
			return e
		}
		e.timers = append(e.timers, time.AfterFunc(d, func() { s.finish(e, o) }))
	}
	if !o.ignoreTimeout {
		d := time.Duration(float64(req.TimeoutMs) * s.opts.TimeoutScale * float64(time.Millisecond))
		timeout := Fail(platform.CodeTimeout).WithMessage(fmt.Sprintf("execution exceeded timeout_ms %d", req.TimeoutMs))
		e.timers = append(e.timers, time.AfterFunc(d, func() { s.finish(e, timeout) }))
	}
	return e
}

func (s *Server) finish(e *execution, o Outcome) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.finishLocked(e, o)
}

// finishLocked moves e to the terminal state o describes. It does nothing if e is already terminal.
func (s *Server) finishLocked(e *execution, o Outcome) {
	if e.x.Status.Terminal() {
		return
	}
	switch o.kind {
	case kindSucceed:
		e.x.Status = platform.ExecutionSucceeded
		e.x.Output = o.output
		if len(e.x.Output) == 0 {
			e.x.Output = e.defaultOutput
		}
	case kindFail:
		err := o.err
		e.x.Status = platform.ExecutionFailed
		e.x.Error = &err
	case kindCancel:
		e.x.Status = platform.ExecutionCancelled
	}
	e.finished = time.Now().UTC()
	e.stopTimers()
	close(e.done)
}

// executionFromPath returns the execution named by {execution_id} if the tenant owns it. The caller
// holds s.mu.
func (s *Server) executionFromPathLocked(r *http.Request, tenant string) *execution {
	e := s.executions[r.PathValue("execution_id")]
	if e == nil || e.tenant != tenant {
		return nil
	}
	return e
}

func (s *Server) getExecution(w http.ResponseWriter, r *http.Request, tenant string) {
	s.mu.Lock()
	e := s.executionFromPathLocked(r, tenant)
	if e == nil {
		s.mu.Unlock()
		writeError(w, errorFor(platform.CodeExecutionNotFound, "execution not found"))
		return
	}
	x := e.snapshot()
	s.mu.Unlock()
	writeJSON(w, http.StatusOK, x)
}

func (s *Server) cancelExecution(w http.ResponseWriter, r *http.Request, tenant string) {
	s.mu.Lock()
	e := s.executionFromPathLocked(r, tenant)
	if e == nil {
		s.mu.Unlock()
		writeError(w, errorFor(platform.CodeExecutionNotFound, "execution not found"))
		return
	}
	e.cancelCalls++
	s.finishLocked(e, Outcome{kind: kindCancel})
	x := e.snapshot()
	s.mu.Unlock()
	writeJSON(w, http.StatusAccepted, x)
}

func (s *Server) getExecutionTrace(w http.ResponseWriter, r *http.Request, tenant string) {
	s.mu.Lock()
	e := s.executionFromPathLocked(r, tenant)
	if e == nil {
		s.mu.Unlock()
		writeError(w, errorFor(platform.CodeExecutionNotFound, "execution not found"))
		return
	}
	step := platform.TraceStep{Type: "model_call", StartedAt: e.started, Summary: "mock model call"}
	if !e.finished.IsZero() {
		ended := e.finished
		step.EndedAt = &ended
	}
	id := e.x.ExecutionID
	s.mu.Unlock()
	writeJSON(w, http.StatusOK, platform.ExecutionTrace{ExecutionID: id, Steps: []platform.TraceStep{step}})
}
