# agent-platform-contracts

Contract repo between Asoar (github.com/sbalaji6/Asoar) and the agent platform service. It holds schemas, the OpenAPI spec, a mock platform server, and a Go client that Asoar imports. Asoar's own code is not in this repo.

Design and plan: docs/design/agent-nodes.md. Read it before changing schemas, the spec, the mock, or the client. Steps 1 and 2 of its plan are implemented here; the other steps belong to the Asoar repo.

- `schemas/` and `api/openapi.yaml` are the source of truth. The mock and client follow them, never the other way round.
- Contracts change additively only once published.
- The binding format in `schemas/agent-node.schema.json` is a placeholder until reconciled with Asoar's `docs/workflow-schema.json` (open question 3).
- Go: standard library first; keep the client free of Asoar-specific types.
