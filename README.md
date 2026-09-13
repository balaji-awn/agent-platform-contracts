# agent-platform-contracts

The contract between **Asoar** (workflow engine) and the **agent platform** service. Asoar lets workflows use agents built on the platform; this repo is the single source of truth both sides build against.

## Layout

| Path | Contents | Status |
|---|---|---|
| `docs/design/agent-nodes.md` | Design, decisions, rejected alternatives, open questions, implementation plan | Done |
| `docs/agent-platform-api-requirements.html` | What the platform service must expose, for the external team: endpoints, error model, idempotency, and sequence flows | Draft |
| `docs/agent-platform-api-reference.html` | Endpoint-by-endpoint reference for `api/openapi.yaml`: every parameter and field, with the reasoning behind each one | Draft |
| `schemas/agent-node.schema.json` | Agent node as stored in Asoar workflow JSON | Draft |
| `schemas/agent-version-contract.schema.json` | Platform response for one agent version | Draft |
| `api/openapi.yaml` | Platform API that Asoar consumes (OpenAPI 3.1) | Draft |
| `api/asoar-designer-api.yaml` | Asoar backend endpoints the designer calls for agents (Step 3). Implemented in the Asoar repo; specified here because it proxies the platform API | Draft |
| `platform/` | Go wire types for the API and the contract digest, shared by the client and the mock | Done |
| `mock/` | Mock platform server for integration tests, scriptable from Go (Step 1) | Done |
| `client/` | Go client that Asoar imports (Step 2) | Done |
| `cmd/mockplatform/` | Runs the mock as a local server for development | Done |
| `internal/jcs/` | RFC 8785 JSON canonicalization, used for the digest and idempotent replay | Done |

Module path: `github.com/sbalaji6/agent-platform-contracts`. Standard library only; `client/` and `platform/` import nothing from `mock/`.

## Testing against the mock

```go
srv := mock.NewTestServer(t, mock.Options{MaxWait: 50 * time.Millisecond})
srv.AddAgentVersion(mock.AlertTriage())
srv.Script(mock.Match{AgentID: "alert-triage"},
	mock.Fail(platform.CodeProviderUnavailable),  // first execution: retryable failure
	mock.Succeed(nil).After(200*time.Millisecond), // second: 202, then poll
)
c, _ := client.New(client.Config{BaseURL: srv.URL})
// ... run the code under test, then assert on srv.Executions() and srv.Calls().
```

See the `mock` package documentation for every outcome (`Succeed`, `Fail`, `Reject`, `Hang`) and option.

Run the tests with `go test -race ./...`.

## Rules for changing contracts

- JSON Schema 2020-12 everywhere; OpenAPI 3.1 for the specs.
- Both specs are self-contained: no `$ref` leaves the document. OpenAPI viewers such as Swagger UI do not fetch external files, so the agent version contract is inlined as an `AgentVersionContract` component in each spec. `schemas/agent-version-contract.schema.json` stays the canonical JSON Schema for code, and `mock/spec_test.go` fails if a copy drifts from it.
- Published contracts change additively only. Removing or renaming a field is a new major version.
- Change the schema or spec first, then the mock and client, in the same PR. `mock/spec_test.go` fails when routes, error codes, or contract fields drift from the spec.
