package mock

import (
	"encoding/json"

	"github.com/sbalaji6/agent-platform-contracts/platform"
)

// AlertTriage returns alert-triage@3, the agent in the example of schemas/agent-node.schema.json.
// Its default output is {"severity":"high","verdict":"suspicious","reasoning":"Mock verdict."}.
func AlertTriage() AgentVersion {
	temperature := 0.2
	return AgentVersion{
		AgentVersionContract: platform.AgentVersionContract{
			AgentID:     "alert-triage",
			Version:     3,
			Name:        "Alert triage",
			Description: "Classifies a security alert by severity and verdict.",
			Tags:        []string{"security", "triage"},
			Owner:       "secops",
			Status:      platform.VersionPublished,
			InputSchema: json.RawMessage(`{
				"type": "object",
				"required": ["alert"],
				"properties": {
					"alert": { "type": "object" },
					"tenant_policy": { "type": "string", "enum": ["strict", "lenient"] }
				}
			}`),
			OutputSchema: json.RawMessage(`{
				"type": "object",
				"required": ["severity", "verdict"],
				"properties": {
					"severity": { "enum": ["low", "medium", "high", "critical"] },
					"verdict": { "enum": ["benign", "suspicious", "malicious"] },
					"reasoning": { "type": "string" }
				}
			}`),
			Limits: platform.Limits{DefaultTimeoutMs: 60000, MaxTimeoutMs: 120000, RecommendedMaxAttempts: 2},
			Model:  &platform.Model{Provider: "mock", Name: "mock-model", Temperature: &temperature},
		},
		DefaultOutput: json.RawMessage(`{"severity":"high","verdict":"suspicious","reasoning":"Mock verdict."}`),
	}
}
