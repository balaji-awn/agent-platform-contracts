# Handoff: agent nodes in Asoar (agent platform integration)

## Repository scope

This repo holds the contract between Asoar and the agent platform, so both services build against one source of truth:

- `schemas/`: JSON Schemas for the agent node and the agent version contract.
- `api/openapi.yaml`: the platform API that Asoar consumes.
- `mock/` (to build, Step 1): a mock platform server for Asoar's integration tests.
- `client/` (to build, Step 2): the Go client that Asoar imports.

Steps 1 and 2 are implemented here. Steps 0 and 3–7 are implemented in the Asoar repo (github.com/sbalaji6/Asoar), which consumes this repo as a Go module. The Asoar codebase is not available in this repo; where this doc describes Asoar code, treat it as context, not something to change here.

## Goal

Let Asoar workflows use agents built in the separate **agent platform service**. Each agent there has a configured input schema, output schema, and model config (provider, model, temperature). Workflow authors should be able to:

1. Find agents in the Asoar designer.
2. Drop them onto the canvas as nodes.
3. Bind their inputs and outputs like any other action.
4. Configure timeout, retry, and error handling per node.
5. Test them from the designer.
6. Publish workflows that execute those agents reliably on the platform at runtime.

## Existing Asoar context (read before changing anything)

- **Engine:** Go. River queue on Postgres (pgx). Compile-once plan cache. Checkpointed resumable runs, with `run_node_result` keyed by `node_path`. Transactional enqueue, cancellation via LISTEN/NOTIFY, event journal, HTTP API.
- **Designer:** React 19, Vite, React Flow, Zustand. The shared contract is `docs/workflow-schema.json`.
- **Existing node types:** `codeBlock`, `ifElse`, `whileLoop`, `subWorkflow`. Bindings carry data flow; edges are control flow only.
- **Versioning:** workflows are published as immutable `workflow_version`, pinned by `id@version`.
- **Runners:** out-of-process Node and Python workers over stdio JSON. Scripts run with **no network or host access**.
- **Not built yet:** authn/authz, secret store, triggers, the integrations/capabilities entities. See `STATUS.md`.

## Final decisions and rationale

### Architecture

| Decision | Why |
|---|---|
| **The designer never calls the agent platform directly.** All calls go through the Asoar backend with a service credential and tenant context. | Keeps platform credentials out of the browser. Gives one place for tenant scoping and catalog caching. |
| **New native engine node type `agent`**, executed by a Go River worker. It is not a `codeBlock`. | Runners have no network. The node needs async polling, idempotency, cancellation, and timeout handling, which belong in the Go worker. |
| **Agents appear in the designer's left panel** in an "Agents" section alongside integration actions. | Authors shouldn't need to know agents run in a different service. |
| **Published agent versions are immutable, and nodes pin `agent_id@agent_version`.** Any change to prompt, model, temperature, or schema creates a new version. | Mirrors `workflow_version` pinning. Prevents silent breakage of downstream bindings and keeps runs reproducible. |
| **Nodes store a contract snapshot and digest.** The snapshot holds the input schema, output schema, and limits, plus a `contract_digest`. | The designer can render and validate without refetching. The digest detects drift. |
| **Publish re-verifies each agent against the platform.** It re-fetches the pinned contract and checks that the version is published (not deprecated or disabled), that the digest matches, and that all bindings are valid. The draft snapshot is never trusted at publish. | The platform is the source of truth. |
| **Upgrading an agent version is explicit.** The node shows an "update available" badge. Upgrading diffs the schemas and highlights broken bindings. | Existing published workflow versions keep running on the old agent version. |
| **Model config is owned by the agent version** and shown read-only. No runtime overrides of model or temperature. | Overrides would make "agent v3" mean different things in different workflows. |
| **Execution settings are owned by the workflow node:** timeout, retries, backoff, and on-error behavior. | These depend on workflow context (real-time vs batch), not on the agent. |
| **JSON Schema 2020-12** for agent input and output schemas, the same dialect as capability action schemas. | Agent nodes and integration actions share one binding and validation path. |

### Platform API that Asoar consumes

**Design time:**
- `GET /agents?search=&tags=&status=published`: lightweight list for the palette.
- `GET /agents/{id}`: agent metadata.
- `GET /agents/{id}/versions`: version list with status.
- `GET /agents/{id}/versions/{v}`: the full contract. Schema is in `agent-version-contract.schema.json`.
- `POST /agents/{id}/versions/{v}/validate`: validate an input without executing.
- `POST /agents/{id}/versions/{v}/test`: sample run, flagged as a test so it's excluded from production metrics.

**Runtime:**
- `POST /executions`: start an execution. Hybrid sync/async via `Prefer: wait=N`: returns 200 with the output if it finishes within the window, otherwise 202 with an `execution_id`.
  - Request body: `agent_id`, `agent_version`, `input`, `context` (tenant_id, workflow_id@version, run_id, node_path), `timeout_ms`, optional `callback_url`.
  - Response: `output`, `usage` (tokens, cost), `model`, `latency_ms`, `trace_id`.
- `GET /executions/{id}`: status and result, used for polling.
- `POST /executions/{id}/cancel`: called when an Asoar run is cancelled or its deadline expires.
- `GET /executions/{id}/trace`: optional, for the runs view.

**Cross-cutting:**
- **Idempotency key:** `run_id + node_path + attempt`. Increment `attempt` only when the engine deliberately retries after a terminal failure. River redelivery after a worker crash reuses the same key, which prevents double execution and double billing. Without `attempt` in the key, a deliberate retry would get back the old failed execution.
- **Output validation:** the platform guarantees output conforms to the output schema, using structured output plus an internal repair loop. Asoar re-validates as defense in depth.
- **Error body:** includes `code`, `message`, and an explicit `retryable: true/false`.
  - Non-retryable: `INPUT_SCHEMA_INVALID`, `OUTPUT_SCHEMA_VIOLATION`, `AGENT_VERSION_NOT_FOUND`, `AGENT_VERSION_DISABLED`.
  - Retryable, with `retry_after`: `PROVIDER_RATE_LIMITED`, `PROVIDER_UNAVAILABLE`, `TIMEOUT`, `QUOTA_EXCEEDED`.
- **Rate limits:** the platform returns rate-limit headers so Asoar can throttle its queue.
- **Journal:** store `execution_id`, `trace_id`, and usage in the event journal.

### Execution semantics

- **Async strategy:** start with polling. The River job submits, gets a 202, then snoozes and polls. Webhook callbacks, where the node parks in a waiting state and the webhook enqueues the continuation, come later.
- **Per-node `execution_policy`:** `timeout_ms`, `max_attempts`, `backoff` (exponential or fixed), and `on_error` (`fail`, `continue`, or `route`). It is frozen with the workflow version.
- **Contract limits:** the agent contract provides `default_timeout_ms`, `max_timeout_ms`, and `recommended_max_attempts`. The designer pre-fills from these and rejects values above the max, and the publish validator checks again.
- **Timeout:** `timeout_ms` is sent in the execution request. Asoar's own deadline is slightly longer to allow for polling delay. When Asoar's deadline expires, it calls cancel.
- **Retries:** only `retryable: true` errors consume further attempts. Non-retryable errors cancel the River job and go straight to on-error handling.
- **Retry layering:** the platform absorbs transient provider errors internally. Asoar retries only on terminal retryable failures. Asoar's default `max_attempts` is 2, which avoids multiplied retries (for example 3 × 3 = 9 LLM calls).
- **`on_error` behavior:**
  - `fail`: the run fails.
  - `continue`: the node output is `null` and the run proceeds. The validator should warn when downstream required inputs bind to this node.
  - `route`: the node gets an `error` output handle. The workflow must have an edge with `sourceHandle: "error"`. Error-branch nodes can bind `$nodes.<id>.error`, whose shape is `NodeError` in the node schema.

### Designer UX

- **Palette:** each agent row shows an info icon on hover. Clicking it expands an inline preview with the description, input and output field names, and an "Add to canvas" button. Only this preview fetches the version contract.
- **Canvas node face:** name, info icon, version badge, "vN available" badge, validation error indicator, and a clock badge when execution settings are non-default. The node has success and error handles when `on_error` is `route`.
- **Inspector:** a right-side panel, not a modal, rendered outside the React Flow viewport. It is driven by `selectedNodeId` and `inspectorTab` in the Zustand store.
  - Clicking the node opens the Inputs tab. The info icon opens the About tab.
  - Tabs: **Inputs**, **Outputs**, **Settings**, **About**, **Test**.
- **Settings tab:** generic across all node types (only defaults and limits differ). It shows which values are overridden, with a reset link, and helper text: "Retries apply to timeouts and provider errors. Invalid input fails right away."
- **About tab:** description, pinned version, model details (read-only), a compare-to-newer-version action, and a link to open the agent in the platform.
- **Icon buttons on nodes:** use the `nodrag nopan` classes and call `stopPropagation()`. Use `NodeToolbar` for any floating per-node UI.

## Rejected alternatives

| Alternative | Reason rejected |
|---|---|
| Modal popup for agent details | Blocks the canvas. Authors need the schema visible while binding. |
| Popovers anchored inside the React Flow viewport | They scale with zoom and get clipped at the edges. |
| Designer calling the platform API directly | Exposes credentials and gives no central tenant scoping or caching. |
| Mutable agents, or nodes floating on "latest" | Silently breaks bindings and makes runs non-reproducible. |
| Per-node overrides of model or temperature | Undermines version pinning and reproducibility. |
| Executing agents as a sandboxed `codeBlock` | Runners have no network, and the async, idempotency, and cancel logic belongs in Go. |
| Idempotency key without `attempt` | Deliberate retries would return the old failed execution. |
| Trusting the draft's contract snapshot at publish | The snapshot can be stale. The platform is the source of truth. |
| Retrying all errors | Schema errors fail identically every time and just burn attempts. |
| Webhook-first async | Deferred, not rejected. Polling via River snooze is simpler to ship first. |

## Constraints

- Code runners have no network access, so agent calls must happen in the Go engine.
- Checkpoints are keyed by `node_path`. Agent node results must write `run_node_result` the same way so resume works.
- River may redeliver jobs, so every platform call must be idempotent.
- `workflow_version` is immutable. Node config, including `execution_policy` and the pinned agent version, is frozen at publish.
- Asoar has no authn/authz or secret store yet. The platform credential must come from config or environment for now.
- Keep the binding format identical to existing nodes.

## Open questions

1. **Tools:** if agents can call tools that hit external systems, do those tools call back into Asoar integration actions (single secrets path, but a two-way protocol) or use the platform's own connectors?
2. **Integration model:** should the agent platform be modeled as an Asoar integration entity (endpoint plus auth), with agents as dynamically discovered actions, or stay a separate concept?
3. **Binding syntax:** the schemas use a placeholder `{ "ref": "$nodes.x.output.y" } | { "literal": ... }` form. Align it with `docs/workflow-schema.json`.
4. **Service auth:** OAuth client credentials or a scoped API key? What is the tenant model between the two services?
5. **Webhook timing:** when should `execution.completed/failed` and `agent.version.published/deprecated` webhooks be added? Catalog cache invalidation depends on the latter.
6. **Batch execution:** is a batch endpoint needed for `whileLoop` fan-out over many items?
7. **Upgrade behavior:** on agent upgrade, should `execution_policy` values that still equal the old defaults move to the new version's defaults?
8. **Platform status:** which platform endpoints exist today? Confirm the contract with the platform codebase before building against it.

## Reference artifacts

These live in this repo:

- **`agent-node.schema.json`:** the agent node as stored in workflow JSON. It contains `config` (pin, digest, snapshot), `bindings`, `execution_policy`, `Backoff`, and `NodeError`, plus a full example.
- **`api/openapi.yaml`:** OpenAPI 3.1 draft of every platform endpoint above, including idempotency, `Prefer: wait`, the error envelope, rate-limit headers, and the planned completion webhook. It references the contract schema by relative path.
- **`agent-version-contract.schema.json`:** the platform's `GET /agents/{id}/versions/{v}` response. The `digest` is sha256 over the RFC 8785 canonical JSON of `{input_schema, output_schema, limits}`, and it excludes model details.

Example `execution_policy`:

```json
{
  "timeout_ms": 90000,
  "max_attempts": 2,
  "backoff": { "type": "exponential", "initial_ms": 2000, "max_ms": 30000 },
  "on_error": "route"
}
```

## Implementation plan

### Step 0: Orient (Asoar repo)

- Read `STATUS.md`, `docs/workflow-schema.json`, the engine's node dispatch and plan compiler, the River job definitions, the checkpoint writer, and the designer's node registry and Zustand store.
- Resolve open question 3 (binding syntax) and update both schema files to match.
- Bring any schema changes back to this repo so the contract stays the single source of truth.

**Done when:** the schemas use the repo's real binding format, and there is a short note in `docs/` describing where the agent node plugs into the engine and designer.

### Step 1: Platform contract and mock server (this repo)

- Review `api/openapi.yaml` against the decisions above and the platform's real routes (open question 8). Fix discrepancies in the spec first, then build the mock from it.
- Build a mock platform server for development and tests (Go, in-repo). It should support configurable latency, a sync-vs-202 cutoff, and scripted error sequences, and it should honor idempotency keys.

**Done when:** integration tests can simulate success, slow success (202 then poll), retryable failures, non-retryable failures, timeouts, and cancellation.

### Step 2: Platform client (this repo, Go)

- Lives under `client/`; Asoar imports it as a Go module.
- Build a typed client with base URL and credential from config, tenant header, and error decoding into a Go error type that carries `Code`, `Retryable`, and `RetryAfter`.
- Add request timeouts. Read the rate-limit headers.

**Done when:** it has unit tests for error classification and header parsing.

### Step 3: Backend proxy endpoints for the designer (Asoar repo)

- `GET /api/agents`, `GET /api/agents/{id}/versions`, `GET /api/agents/{id}/versions/{v}`, `POST /api/agents/{id}/versions/{v}/test`.
- Cache the catalog list with a short TTL, invalidated by webhook later.

**Done when:** the designer can list, inspect, and test agents without any platform credentials in the browser.

### Step 4: Workflow schema and validation (Asoar repo)

- Add the `agent` node type, `execution_policy`, and `sourceHandle: "error"` edges to `docs/workflow-schema.json`. Generate or update the Go types.
- Extend the publish validator. For each agent node:
  - Re-fetch the contract.
  - Check that the status is `published`.
  - Check that the digest matches.
  - Check that `timeout_ms <= max_timeout_ms`.
  - Check that `route` has an error edge.
  - Type-check bindings against the input schema and the upstream output schemas.
  - Warn when downstream required inputs bind to a `continue` node.
- Include agent nodes in the compiled plan.

**Done when:** publish rejects each violation with a clear message, and it has table-driven tests.

### Step 5: Engine executor for agent nodes (Asoar repo)

- Add a River job for agent nodes:
  1. Resolve bindings.
  2. Build the idempotency key from `run_id + node_path + attempt`.
  3. `POST /executions` with `Prefer: wait`.
  4. On 202, snooze and poll `GET /executions/{id}`.
  5. Enforce Asoar's deadline, which is slightly longer than `timeout_ms`, and call cancel when it expires.
- Map `max_attempts` and `backoff` onto River retry. Retryable errors return an error, so River retries with the backoff. Non-retryable errors cancel the job and apply `on_error`.
- Propagate run cancellation (LISTEN/NOTIFY) to `POST /executions/{id}/cancel`.
- Re-validate the output against the output schema. Write `run_node_result` keyed by `node_path`. Journal `execution_id`, `trace_id`, usage, and latency.
- Implement the `on_error` outcomes: fail the run; write a null output and continue; or write `NodeError` and follow the error edge.

**Done when:** integration tests against the mock pass for each of the following:
- A worker crash mid-execution followed by redelivery produces exactly one platform execution.
- Retryable errors retry up to `max_attempts`.
- Non-retryable errors skip the remaining attempts.
- A timeout triggers cancel.
- Run cancellation triggers cancel.
- All three `on_error` paths behave as specified.

### Step 6: Designer (Asoar repo)

- **Palette:** "Agents" section, hover info icon, inline preview with "Add to canvas".
- **Agent node component:** on drop, pin the latest published version and store the snapshot and digest. Pre-fill `execution_policy` from the contract limits. Show the badges: version, update available, validation error, and clock.
- **Inspector shell:** outside the viewport, with store-driven `selectedNodeId` and `inspectorTab`. Tabs: Inputs, Outputs, Settings, About, Test.
- **Settings tab:** generic across node types. Overridden indicators and reset links. Inline validation against the contract limits. Selecting `route` adds or removes the error handle and its edges.
- **Test tab:** sample input, with the option to reuse captured upstream output. Shows output, usage, and latency.
- **Upgrade flow:** schema diff and highlighting of broken bindings.

**Done when:** an author can find an agent, drop it, bind it, configure it, test it, and publish a workflow end to end against the mock platform.

### Step 7: Hardening (later, Asoar repo)

- Webhooks: execution completion to park and resume nodes without polling, and version lifecycle events to invalidate the catalog cache and flag deprecated pins.
- Cost rollups per workflow run in the runs view, with a deep link to the platform trace.
- Revisit open questions 1, 2, 6, and 7.
