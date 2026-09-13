package client

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/sbalaji6/agent-platform-contracts/platform"
)

// ListAgentsParams filters listAgents. Zero fields are not sent.
type ListAgentsParams struct {
	// Search is a case-insensitive substring of agent_id, name, or description.
	Search string
	// Tags keeps agents that have every tag.
	Tags []string
	// Status keeps agents with at least one version in this status. Default published.
	Status platform.AgentVersionStatus
	// Cursor is the NextCursor of a previous page.
	Cursor string
	// Limit is the page size, 1 to 200. Default 50.
	Limit int
}

// ListAgents returns one page of the agents visible to the tenant.
func (t *TenantClient) ListAgents(ctx context.Context, p ListAgentsParams) (*platform.AgentList, *Response, error) {
	q := url.Values{}
	if p.Search != "" {
		q.Set("search", p.Search)
	}
	if len(p.Tags) > 0 {
		q.Set("tags", strings.Join(p.Tags, ","))
	}
	if p.Status != "" {
		q.Set("status", string(p.Status))
	}
	if p.Cursor != "" {
		q.Set("cursor", p.Cursor)
	}
	if p.Limit > 0 {
		q.Set("limit", strconv.Itoa(p.Limit))
	}
	return call[platform.AgentList](ctx, t, request{method: http.MethodGet, path: "/agents", query: q})
}

// GetAgent returns an agent's metadata.
func (t *TenantClient) GetAgent(ctx context.Context, agentID string) (*platform.AgentSummary, *Response, error) {
	return call[platform.AgentSummary](ctx, t, request{method: http.MethodGet, path: agentPath(agentID)})
}

// ListAgentVersions returns every version of an agent, newest first.
func (t *TenantClient) ListAgentVersions(ctx context.Context, agentID string) (*platform.AgentVersionList, *Response, error) {
	return call[platform.AgentVersionList](ctx, t, request{method: http.MethodGet, path: agentPath(agentID) + "/versions"})
}

// GetAgentVersion returns the full contract of an agent version.
func (t *TenantClient) GetAgentVersion(ctx context.Context, agentID string, version int) (*platform.AgentVersionContract, *Response, error) {
	return call[platform.AgentVersionContract](ctx, t, request{method: http.MethodGet, path: versionPath(agentID, version)})
}

// TestAgentVersion runs a test execution, excluded from production metrics. It waits up to wait
// (rounded up to whole seconds; zero does not wait) for the result, like CreateExecution.
func (t *TenantClient) TestAgentVersion(ctx context.Context, agentID string, version int, req platform.TestRequest, wait time.Duration) (*platform.Execution, *Response, error) {
	return call[platform.Execution](ctx, t, request{
		method: http.MethodPost,
		path:   versionPath(agentID, version) + "/test",
		body:   req,
		wait:   wait,
	})
}

// CreateExecution starts an execution, or replays the one already recorded for idempotencyKey. It
// waits up to wait (rounded up to whole seconds; zero does not wait) for a terminal state. On 200
// the execution is terminal, possibly failed (see ExecutionError); on 202 (resp.Accepted) it is still
// running and the caller polls GetExecution. The request timeout is wait plus Config.WaitMargin.
func (t *TenantClient) CreateExecution(ctx context.Context, idempotencyKey string, req platform.ExecutionRequest, wait time.Duration) (*platform.Execution, *Response, error) {
	if idempotencyKey == "" {
		return nil, nil, errors.New("client: idempotency key is required")
	}
	return call[platform.Execution](ctx, t, request{
		method:         http.MethodPost,
		path:           "/executions",
		body:           req,
		idempotencyKey: idempotencyKey,
		wait:           wait,
	})
}

// GetExecution returns the current state of an execution. A failed execution is returned with a
// nil error; see ExecutionError.
func (t *TenantClient) GetExecution(ctx context.Context, executionID string) (*platform.Execution, *Response, error) {
	return call[platform.Execution](ctx, t, request{method: http.MethodGet, path: executionPath(executionID)})
}

// CancelExecution cancels an execution. It is idempotent, and cancelling a terminal execution
// returns it unchanged. The returned execution may still be running; poll GetExecution.
func (t *TenantClient) CancelExecution(ctx context.Context, executionID string) (*platform.Execution, *Response, error) {
	return call[platform.Execution](ctx, t, request{method: http.MethodPost, path: executionPath(executionID) + "/cancel"})
}

// GetExecutionTrace returns the intermediate steps of an execution.
func (t *TenantClient) GetExecutionTrace(ctx context.Context, executionID string) (*platform.ExecutionTrace, *Response, error) {
	return call[platform.ExecutionTrace](ctx, t, request{method: http.MethodGet, path: executionPath(executionID) + "/trace"})
}

func agentPath(agentID string) string {
	return "/agents/" + url.PathEscape(agentID)
}

func versionPath(agentID string, version int) string {
	return agentPath(agentID) + "/versions/" + strconv.Itoa(version)
}

func executionPath(executionID string) string {
	return "/executions/" + url.PathEscape(executionID)
}
