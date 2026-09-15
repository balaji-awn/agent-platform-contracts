package mock

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/sbalaji6/agent-platform-contracts/platform"
)

// AgentVersion is an agent version to seed into the catalog.
type AgentVersion struct {
	platform.AgentVersionContract
	// DefaultOutput is the output of executions of this version that no script matches. Default {}.
	DefaultOutput json.RawMessage `json:"default_output,omitempty"`
}

type agent struct {
	id       string
	tenants  []string // nil means every tenant
	versions map[int]*AgentVersion
}

func (a *agent) visibleTo(tenant string) bool {
	return a.tenants == nil || slices.Contains(a.tenants, tenant)
}

// sortedVersions returns the versions, newest first.
func (a *agent) sortedVersions() []*AgentVersion {
	vs := make([]*AgentVersion, 0, len(a.versions))
	for _, v := range a.versions {
		vs = append(vs, v)
	}
	slices.SortFunc(vs, func(x, y *AgentVersion) int { return y.Version - x.Version })
	return vs
}

func (a *agent) summary() platform.AgentSummary {
	vs := a.sortedVersions()
	top := vs[0]
	sum := platform.AgentSummary{
		AgentID:     a.id,
		Name:        top.Name,
		Description: top.Description,
		Tags:        top.Tags,
		Owner:       top.Owner,
	}
	for _, v := range vs {
		if v.Status == platform.VersionPublished {
			n := v.Version
			sum.LatestVersion = &n
			break
		}
	}
	return sum
}

// AddAgentVersion adds v to the catalog, replacing any version with the same agent ID and number.
// It defaults Status to published, PublishedAt to now, and DefaultOutput to {}, and computes Digest
// when it is empty. It returns an error if the contract is invalid, or if Digest is set and does not
// match the computed digest.
func (s *Server) AddAgentVersion(v AgentVersion) error {
	c := &v.AgentVersionContract
	switch {
	case c.AgentID == "":
		return errors.New("mock: agent_id is required")
	case c.Version < 1:
		return fmt.Errorf("mock: %s: version must be at least 1", c.AgentID)
	case c.Name == "":
		return fmt.Errorf("mock: %s@%d: name is required", c.AgentID, c.Version)
	case !isJSONObject(c.InputSchema) || !isJSONObject(c.OutputSchema):
		return fmt.Errorf("mock: %s@%d: input_schema and output_schema must be JSON objects", c.AgentID, c.Version)
	case c.Limits.DefaultTimeoutMs < 1000 || c.Limits.MaxTimeoutMs < c.Limits.DefaultTimeoutMs:
		return fmt.Errorf("mock: %s@%d: limits need 1000 <= default_timeout_ms <= max_timeout_ms", c.AgentID, c.Version)
	}
	if c.Status == "" {
		c.Status = platform.VersionPublished
	}
	if c.PublishedAt.IsZero() {
		c.PublishedAt = time.Now().UTC().Truncate(time.Second)
	}
	if len(v.DefaultOutput) == 0 {
		v.DefaultOutput = json.RawMessage(`{}`)
	}
	digest, err := c.ComputeDigest()
	if err != nil {
		return fmt.Errorf("mock: %s@%d: %w", c.AgentID, c.Version, err)
	}
	if c.Digest != "" && c.Digest != digest {
		return fmt.Errorf("mock: %s@%d: digest %s does not match computed %s", c.AgentID, c.Version, c.Digest, digest)
	}
	c.Digest = digest

	s.mu.Lock()
	defer s.mu.Unlock()
	a := s.agents[c.AgentID]
	if a == nil {
		a = &agent{id: c.AgentID, versions: map[int]*AgentVersion{}}
		s.agents[c.AgentID] = a
	}
	a.versions[c.Version] = &v
	return nil
}

// SetVersionStatus changes the status of a seeded version, for example to disable it mid-test.
func (s *Server) SetVersionStatus(agentID string, version int, status platform.AgentVersionStatus) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	v := s.lookupVersionLocked(agentID, version)
	if v == nil {
		return fmt.Errorf("mock: no agent version %s@%d", agentID, version)
	}
	v.Status = status
	return nil
}

// RestrictAgent makes an agent visible only to the given tenants. Other tenants get 404.
func (s *Server) RestrictAgent(agentID string, tenants ...string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	a := s.agents[agentID]
	if a == nil {
		return fmt.Errorf("mock: no agent %s", agentID)
	}
	a.tenants = append([]string{}, tenants...)
	return nil
}

func (s *Server) lookupVersionLocked(agentID string, version int) *AgentVersion {
	if a := s.agents[agentID]; a != nil {
		return a.versions[version]
	}
	return nil
}

// visibleVersionLocked returns the version if it exists and the tenant can see its agent.
func (s *Server) visibleVersionLocked(tenant, agentID string, version int) *AgentVersion {
	a := s.agents[agentID]
	if a == nil || !a.visibleTo(tenant) {
		return nil
	}
	return a.versions[version]
}

func isJSONObject(raw json.RawMessage) bool {
	var m map[string]json.RawMessage
	return json.Unmarshal(raw, &m) == nil && m != nil
}

func (s *Server) listAgents(w http.ResponseWriter, r *http.Request, tenant string) {
	q := r.URL.Query()
	status := platform.AgentVersionStatus(q.Get("status"))
	if status == "" {
		status = platform.VersionPublished
	}
	if status != platform.VersionPublished && status != platform.VersionDeprecated {
		writeError(w, errorFor(platform.CodeRequestInvalid, "invalid request",
			FieldError{Path: "/status", Message: "must be published or deprecated"}))
		return
	}
	limit := 50
	if v := q.Get("limit"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 || n > 200 {
			writeError(w, errorFor(platform.CodeRequestInvalid, "invalid request",
				FieldError{Path: "/limit", Message: "must be an integer from 1 to 200"}))
			return
		}
		limit = n
	}
	offset := 0
	if v := q.Get("cursor"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 0 {
			writeError(w, errorFor(platform.CodeRequestInvalid, "invalid request",
				FieldError{Path: "/cursor", Message: "is not a valid cursor"}))
			return
		}
		offset = n
	}
	search := strings.ToLower(q.Get("search"))
	var tags []string
	for _, v := range q["tags"] {
		for _, t := range strings.Split(v, ",") {
			if t = strings.TrimSpace(t); t != "" {
				tags = append(tags, t)
			}
		}
	}

	s.mu.Lock()
	var matched []platform.AgentSummary
	for _, a := range s.agents {
		if !a.visibleTo(tenant) {
			continue
		}
		hasStatus := false
		for _, v := range a.versions {
			hasStatus = hasStatus || v.Status == status
		}
		if !hasStatus {
			continue
		}
		sum := a.summary()
		if search != "" && !strings.Contains(strings.ToLower(sum.AgentID), search) &&
			!strings.Contains(strings.ToLower(sum.Name), search) &&
			!strings.Contains(strings.ToLower(sum.Description), search) {
			continue
		}
		if !containsAll(sum.Tags, tags) {
			continue
		}
		matched = append(matched, sum)
	}
	s.mu.Unlock()

	slices.SortFunc(matched, func(x, y platform.AgentSummary) int { return strings.Compare(x.AgentID, y.AgentID) })
	page := platform.AgentList{Items: []platform.AgentSummary{}}
	if offset < len(matched) {
		end := min(offset+limit, len(matched))
		page.Items = matched[offset:end]
		if end < len(matched) {
			next := strconv.Itoa(end)
			page.NextCursor = &next
		}
	}
	writeJSON(w, http.StatusOK, page)
}

func containsAll(have, want []string) bool {
	for _, t := range want {
		if !slices.Contains(have, t) {
			return false
		}
	}
	return true
}

func (s *Server) getAgent(w http.ResponseWriter, r *http.Request, tenant string) {
	s.mu.Lock()
	a := s.agents[r.PathValue("agent_id")]
	if a == nil || !a.visibleTo(tenant) {
		s.mu.Unlock()
		writeError(w, errorFor(platform.CodeAgentNotFound, "agent not found"))
		return
	}
	sum := a.summary()
	s.mu.Unlock()
	writeJSON(w, http.StatusOK, sum)
}

func (s *Server) listAgentVersions(w http.ResponseWriter, r *http.Request, tenant string) {
	s.mu.Lock()
	a := s.agents[r.PathValue("agent_id")]
	if a == nil || !a.visibleTo(tenant) {
		s.mu.Unlock()
		writeError(w, errorFor(platform.CodeAgentNotFound, "agent not found"))
		return
	}
	list := platform.AgentVersionList{Items: []platform.AgentVersionSummary{}}
	for _, v := range a.sortedVersions() {
		list.Items = append(list.Items, platform.AgentVersionSummary{
			Version:     v.Version,
			Status:      v.Status,
			Digest:      v.Digest,
			PublishedAt: v.PublishedAt,
		})
	}
	s.mu.Unlock()
	writeJSON(w, http.StatusOK, list)
}

// versionFromPath resolves {agent_id} and {version} for the tenant, writing the error response and
// returning nil when it cannot. The caller must not hold s.mu.
func (s *Server) versionFromPath(w http.ResponseWriter, r *http.Request, tenant string) *AgentVersion {
	n, err := strconv.Atoi(r.PathValue("version"))
	if err != nil || n < 1 {
		writeError(w, errorFor(platform.CodeRequestInvalid, "invalid request",
			FieldError{Path: "/version", Message: "must be a positive integer"}))
		return nil
	}
	s.mu.Lock()
	v := s.visibleVersionLocked(tenant, r.PathValue("agent_id"), n)
	var cp AgentVersion
	if v != nil {
		cp = *v
	}
	s.mu.Unlock()
	if v == nil {
		writeError(w, errorFor(platform.CodeAgentVersionNotFound, "agent version not found"))
		return nil
	}
	return &cp
}

func (s *Server) getAgentVersion(w http.ResponseWriter, r *http.Request, tenant string) {
	if v := s.versionFromPath(w, r, tenant); v != nil {
		writeJSON(w, http.StatusOK, v.AgentVersionContract)
	}
}
