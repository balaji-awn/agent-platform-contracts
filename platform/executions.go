package platform

import (
	"encoding/json"
	"time"
)

// ExecutionStatus is the state of an execution.
type ExecutionStatus string

const (
	ExecutionQueued    ExecutionStatus = "queued"
	ExecutionRunning   ExecutionStatus = "running"
	ExecutionSucceeded ExecutionStatus = "succeeded"
	ExecutionFailed    ExecutionStatus = "failed"
	ExecutionCancelled ExecutionStatus = "cancelled"
)

// Terminal reports whether s is a final state.
func (s ExecutionStatus) Terminal() bool {
	return s == ExecutionSucceeded || s == ExecutionFailed || s == ExecutionCancelled
}

// ExecutionRequest is the body of createExecution.
type ExecutionRequest struct {
	AgentID      string           `json:"agent_id"`
	AgentVersion int              `json:"agent_version"`
	Input        json.RawMessage  `json:"input"`
	Context      ExecutionContext `json:"context"`
	// TimeoutMs applies per attempt, from execution creation, and must not exceed the version's
	// max_timeout_ms.
	TimeoutMs int64 `json:"timeout_ms"`
}

// ExecutionContext identifies the caller's workflow run and node.
type ExecutionContext struct {
	// WorkflowID is id@version, e.g. wf_phishing@7.
	WorkflowID string `json:"workflow_id"`
	RunID      string `json:"run_id"`
	NodePath   string `json:"node_path"`
	Attempt    int    `json:"attempt,omitempty"`
}

// Execution is the state of one execution.
type Execution struct {
	ExecutionID  string          `json:"execution_id"`
	Status       ExecutionStatus `json:"status"`
	AgentID      string          `json:"agent_id"`
	AgentVersion int             `json:"agent_version"`
	// Output is set when Status is succeeded. It is the JSON text "null" for a null output and nil
	// when absent.
	Output json.RawMessage `json:"output,omitempty"`
	// Error is set when Status is failed.
	Error   *Error `json:"error,omitempty"`
	TraceID string `json:"trace_id,omitempty"`
}

// ExecutionTrace is the body of getExecutionTrace.
type ExecutionTrace struct {
	ExecutionID string      `json:"execution_id"`
	Steps       []TraceStep `json:"steps"`
}

// TraceStep is one intermediate step of an execution.
type TraceStep struct {
	Type      string     `json:"type"`
	StartedAt time.Time  `json:"started_at"`
	EndedAt   *time.Time `json:"ended_at,omitempty"`
	Summary   string     `json:"summary,omitempty"`
}
