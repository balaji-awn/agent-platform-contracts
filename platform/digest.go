package platform

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/sbalaji6/agent-platform-contracts/internal/jcs"
)

// ContractDigest returns the digest of an agent version contract: "sha256:" followed by the hex
// sha256 of the RFC 8785 canonical JSON of {input_schema, output_schema, limits}. Pass the JSON
// exactly as served so fields this package does not model are included.
func ContractDigest(inputSchema, outputSchema, limits json.RawMessage) (string, error) {
	parts := []struct {
		name string
		raw  json.RawMessage
	}{
		{"input_schema", inputSchema},
		{"output_schema", outputSchema},
		{"limits", limits},
	}
	var doc bytes.Buffer
	doc.WriteByte('{')
	for i, p := range parts {
		if len(bytes.TrimSpace(p.raw)) == 0 {
			return "", fmt.Errorf("platform: digest: %s is empty", p.name)
		}
		if i > 0 {
			doc.WriteByte(',')
		}
		fmt.Fprintf(&doc, "%q:", p.name)
		doc.Write(p.raw)
	}
	doc.WriteByte('}')
	canon, err := jcs.Transform(doc.Bytes())
	if err != nil {
		return "", fmt.Errorf("platform: digest: %w", err)
	}
	sum := sha256.Sum256(canon)
	return "sha256:" + hex.EncodeToString(sum[:]), nil
}

// ComputeDigest returns the digest of c's input schema, output schema, and limits. It covers only
// the limits fields this package models; use ContractDigest with the served JSON to verify a
// contract fetched from the platform.
func (c *AgentVersionContract) ComputeDigest() (string, error) {
	if c == nil {
		return "", errors.New("platform: digest: nil contract")
	}
	limits, err := json.Marshal(c.Limits)
	if err != nil {
		return "", err
	}
	return ContractDigest(c.InputSchema, c.OutputSchema, limits)
}
