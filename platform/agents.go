// Package platform holds the wire types of the agent platform API (api/openapi.yaml and
// schemas/agent-version-contract.schema.json), shared by the client and the mock.
package platform

import (
	"encoding/json"
	"time"
)

// Header names used by the API.
const (
	HeaderTenantID           = "X-Tenant-ID"
	HeaderIdempotencyKey     = "Idempotency-Key"
	HeaderPrefer             = "Prefer"
	HeaderPreferenceApplied  = "Preference-Applied"
	HeaderRetryAfter         = "Retry-After"
	HeaderRateLimitLimit     = "RateLimit-Limit"
	HeaderRateLimitRemaining = "RateLimit-Remaining"
	HeaderRateLimitReset     = "RateLimit-Reset"
)

// AgentVersionStatus is the lifecycle status of an agent version.
type AgentVersionStatus string

const (
	// VersionPublished versions can be pinned and executed.
	VersionPublished AgentVersionStatus = "published"
	// VersionDeprecated versions still execute, but the designer warns and blocks new pins.
	VersionDeprecated AgentVersionStatus = "deprecated"
	// VersionDisabled versions are rejected with AGENT_VERSION_DISABLED.
	VersionDisabled AgentVersionStatus = "disabled"
)

// AgentSummary is the palette entry returned by listAgents and getAgent.
type AgentSummary struct {
	AgentID     string   `json:"agent_id"`
	Name        string   `json:"name"`
	Description string   `json:"description,omitempty"`
	Tags        []string `json:"tags,omitempty"`
	Owner       string   `json:"owner,omitempty"`
	// LatestVersion is the highest published version, or nil if the agent has none.
	LatestVersion *int `json:"latest_version"`
}

// AgentList is one page of listAgents.
type AgentList struct {
	Items []AgentSummary `json:"items"`
	// NextCursor is nil on the last page.
	NextCursor *string `json:"next_cursor"`
}

// AgentVersionSummary is one entry of listAgentVersions.
type AgentVersionSummary struct {
	Version     int                `json:"version"`
	Status      AgentVersionStatus `json:"status"`
	Digest      string             `json:"digest"`
	PublishedAt time.Time          `json:"published_at"`
}

// AgentVersionList is the body of listAgentVersions, newest first.
type AgentVersionList struct {
	Items []AgentVersionSummary `json:"items"`
}

// AgentVersionContract is the body of getAgentVersion (schemas/agent-version-contract.schema.json).
type AgentVersionContract struct {
	AgentID      string             `json:"agent_id"`
	Version      int                `json:"version"`
	Name         string             `json:"name"`
	Description  string             `json:"description,omitempty"`
	Tags         []string           `json:"tags,omitempty"`
	Owner        string             `json:"owner,omitempty"`
	Status       AgentVersionStatus `json:"status"`
	Deprecation  *Deprecation       `json:"deprecation,omitempty"`
	InputSchema  json.RawMessage    `json:"input_schema"`
	OutputSchema json.RawMessage    `json:"output_schema"`
	Limits       Limits             `json:"limits"`
	Model        *Model             `json:"model,omitempty"`
	Digest       string             `json:"digest"`
	PublishedAt  time.Time          `json:"published_at"`
}

// Deprecation describes why and since when a version is deprecated.
type Deprecation struct {
	Since              *time.Time `json:"since,omitempty"`
	ReplacementVersion int        `json:"replacement_version,omitempty"`
	Message            string     `json:"message,omitempty"`
}

// Limits are the execution limits of an agent version.
type Limits struct {
	DefaultTimeoutMs       int64 `json:"default_timeout_ms"`
	MaxTimeoutMs           int64 `json:"max_timeout_ms"`
	RecommendedMaxAttempts int   `json:"recommended_max_attempts,omitempty"`
}

// Model describes the model an agent version runs. It is informational and not part of the digest.
type Model struct {
	Provider    string   `json:"provider,omitempty"`
	Name        string   `json:"name,omitempty"`
	Temperature *float64 `json:"temperature,omitempty"`
	MaxTokens   int      `json:"max_tokens,omitempty"`
}

// TestRequest is the body of testAgentVersion. TimeoutMs 0 means the version's default timeout.
type TestRequest struct {
	Input     json.RawMessage `json:"input"`
	TimeoutMs int64           `json:"timeout_ms,omitempty"`
}
