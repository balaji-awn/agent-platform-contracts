# SOAR ↔ AI Agent Platform Integration — Requirements Specification

| | |
|---|---|
| **Version** | 1.0-draft |
| **Status** | Draft |
| **Date** | 2026-09-15 |
| **Scope** | Capabilities a SOAR workflow engine requires from an AI agent platform |

---

## 1. Introduction

### 1.1 Purpose

This document specifies the capabilities a SOAR (security orchestration, automation, and response)
workflow engine requires from an AI agent platform, so that workflow authors can discover agents, bind them
into workflows as typed steps, and run them reliably in production.

It is written independently of any particular platform API. Each requirement describes a capability and its
acceptance criteria; the traceability matrix in section 20 records which platform endpoints, fields, and
events satisfy it.

### 1.2 Scope

**In scope**

- Agent discovery and version management.
- The agent contract: input schema, output schema, execution limits, lifecycle.
- Starting executions, receiving results over Server-Sent Events (SSE), recovery, cancellation, and
  timeouts.
- Error semantics, output guarantees, usage reporting, and data handling.

**Out of scope**

- The internal design of the SOAR (section 19 lists its responsibilities for context only).
- User interface design.
- Implementation of either system.

### 1.3 Conventions

**Requirement levels**

| Level | Meaning |
|---|---|
| **MUST** | The integration depends on this capability. If absent, it is a gap with a stated consequence. |
| **SHOULD** | Strongly preferred. If absent, the SOAR requires a workaround. |
| **MAY** | Useful but optional. |

**Identifiers.** Each requirement has a stable identifier of the form `GROUP-NN`.

**★ Exactly-once critical.** Marks the requirements that determine whether the SOAR can guarantee that a
retried or redelivered job neither loses a result nor runs an agent twice. Section 18 defines how their
coverage determines that guarantee.

**Requirement format.** Each requirement states its purpose, inputs, outputs, and required behaviour, and
closes with acceptance criteria.

## 2. Terminology

| Term | Definition |
|---|---|
| SOAR | The workflow engine consuming the platform. Workflows are graphs of nodes; node work is executed as jobs by workers pulling from a durable queue. |
| Platform | The AI agent platform providing agents and executing them. |
| Agent | A named capability hosted on the platform. |
| Agent version | An immutable, published revision of an agent. Workflows pin an exact version. |
| Contract | What an agent version promises: input schema, output schema, execution limits, model configuration, lifecycle status, and a fingerprint. |
| Execution | One run of one agent version with one input. Platforms may call this a run, job, or invocation. |
| Node occurrence | One execution position of a workflow node within a workflow run, including loop iteration and sub-workflow nesting. A single node inside a loop has many occurrences. |
| Tenant | The customer boundary. Agents and executions are scoped to a tenant. |
| Intent record | The record the SOAR persists before starting an execution, used to decide what a retried job does (section 19). |

## 3. Context and assumptions

1. **Call direction.** All platform calls originate from the SOAR backend, authenticated as a service. The
   SOAR's browser-based designer never calls the platform directly.
2. **Result delivery.** The platform delivers execution results over Server-Sent Events.
3. **Delivery guarantees.** The SOAR's job queue delivers at least once: a worker may crash mid-job, and the
   job is redelivered to another worker. Every requirement in this document is framed against that
   failure.
4. **Version pinning.** Workflows reference an exact agent version, never a floating "latest", so a
   published workflow behaves identically on every run.
5. **Side effects.** Some agents only analyse data; others take actions such as blocking, disabling, or
   isolating. Running an action-taking agent twice is materially worse than losing a result.

## 4. General (GEN)

### GEN-01 · Service authentication · MUST

- **Purpose:** the SOAR backend calls the platform as a service, not on behalf of an interactive user.
- **Required behaviour:** a server-to-server credential type, with a defined way to obtain, scope, rotate,
  and present it on each request.
- **Acceptance:** a service-level authentication scheme usable without an interactive user is defined.

### GEN-02 · Tenant scoping · MUST

- **Purpose:** one SOAR backend serves many tenants; agents and executions must not leak across them.
- **Inputs:** the tenant identity, conveyed by header, token claim, path, or credential.
- **Required behaviour:** every catalog, contract, execution, and stream operation is tenant-scoped. A
  resource belonging to another tenant should be reported as not found rather than forbidden, so its
  existence is not confirmed.
- **Acceptance:** tenant scoping is defined for every operation the SOAR uses.

### GEN-03 · API versioning and compatibility · SHOULD

- **Purpose:** the SOAR keeps working as the platform evolves.
- **Required behaviour:** an API version indicator, such as a base path or header, and a compatibility
  policy — for example, that response objects may gain fields and clients ignore unknown fields.
- **Acceptance:** versioning and the change policy are documented.

### GEN-04 · Rate-limit signalling · SHOULD

- **Purpose:** the SOAR paces its job queue before it is throttled.
- **Outputs:** request limit, remaining budget, and reset time on responses, including error responses; a
  throttling status with a retry-after value.
- **Acceptance:** limit signals and the throttling response are documented.

## 5. Catalog (CAT)

### CAT-01 · List agents · MUST

- **Purpose:** populate the designer's agent palette.
- **Inputs:** tenant; optional search text, tag filters, lifecycle status filter, page cursor, page size.
- **Outputs, per agent:** agent id, display name, description, tags, owner, latest published version; a
  cursor for the next page.
- **Required behaviour:** tenant-scoped; stable ordering across pages; lightweight, without schemas.
- **Acceptance:** agents are listable with the outputs above. Absence of search, filtering, or pagination is
  partial coverage.

### CAT-02 · Get one agent · MAY

- **Purpose:** refresh a single palette entry, or obtain an agent's latest published version without
  listing the catalog.
- **Inputs:** agent id.
- **Outputs:** the same fields as one CAT-01 entry.
- **Acceptance:** a single agent is retrievable by id.

### CAT-03 · List an agent's versions · MUST

- **Purpose:** show that a newer version exists for a pinned node, drive the upgrade flow, and warn about a
  pin that has since been deprecated or disabled.
- **Inputs:** agent id.
- **Outputs, per version:** version identifier, lifecycle status, published timestamp, and fingerprint where
  available (CON-05).
- **Required behaviour:** includes versions in every lifecycle status, with an ordering from which "newer"
  can be determined.
- **Acceptance:** all versions of an agent are listable with their status.

## 6. Contract (CON)

### CON-01 · Get a version's contract · MUST

- **Purpose:** the designer renders inputs and outputs and type-checks bindings; the SOAR stores a snapshot
  of the contract on the workflow node; publish re-fetches the contract to verify the snapshot is still
  accurate.
- **Inputs:** agent id and exact version.
- **Outputs:** input schema, output schema, execution limits (CON-04), model configuration (CON-08),
  lifecycle status (CON-06), published timestamp, fingerprint (CON-05).
- **Required behaviour:** returned for a version in any lifecycle status.
- **Acceptance:** the full contract of an exact version is retrievable.

### CON-02 · Immutable published versions · MUST

- **Purpose:** a published workflow must behave identically on every run; a changed contract would break
  bindings without warning.
- **Required behaviour:** a published version's schemas, limits, and model configuration never change. Any
  change produces a new version.
- **Acceptance:** immutability is stated, or implied by the versioning rules. A mutable "latest" reference
  alone does not satisfy this requirement.

### CON-03 · Declared schema dialect · MUST

- **Purpose:** the SOAR validates bindings and inputs locally, and must use a validator that agrees with the
  platform's.
- **Outputs:** the schema language and dialect (for example JSON Schema 2020-12), and any restrictions or
  extensions to the keywords agent schemas may use.
- **Acceptance:** the dialect is stated. Schemas present without a stated dialect are undocumented coverage.

### CON-04 · Execution limits · SHOULD

- **Purpose:** pre-fill a node's timeout and retry settings, and reject values the platform would refuse.
- **Outputs:** default timeout, maximum timeout, recommended number of attempts.
- **Acceptance:** limits are available per version, or documented platform-wide.

### CON-05 · Contract fingerprint · SHOULD

- **Purpose:** detect drift between a node's stored contract snapshot and the platform's current contract.
- **Outputs:** a digest or hash, and the contract fields it covers.
- **Acceptance:** a fingerprint is provided. If absent, the SOAR computes its own over the fields bindings
  depend on.

### CON-06 · Lifecycle status · MUST

- **Purpose:** decide whether a version may be pinned, executed, and published against.
- **Outputs:** a status per version covering at least: usable; deprecated (still executes, but authors are
  steered away); disabled (cannot execute). Optionally, deprecation details such as a replacement version
  and a message.
- **Acceptance:** statuses and their effect on execution are defined.

### CON-07 · Side-effect declaration · SHOULD

- **Purpose:** decide how an ambiguous execution outcome is handled. A read-only agent may be retried
  safely; an action-taking agent must not run twice.
- **Outputs:** whether an agent version is read-only or can cause external side effects, or the tools and
  actions it can invoke.
- **Acceptance:** side effects are declared per agent or per version. If absent, the SOAR records the
  classification itself.

### CON-08 · Model configuration visibility · MAY

- **Purpose:** show authors, read-only, which model an agent version runs.
- **Outputs:** provider, model name, key parameters.
- **Required behaviour:** not overridable per execution (EXE-01).
- **Acceptance:** model configuration is exposed on the contract.

## 7. Starting an execution (EXE)

### EXE-01 · Start an execution of a pinned version · MUST

- **Purpose:** run an agent as a workflow step.
- **Inputs:** agent id, exact version, input, timeout (TMO-01).
- **Required behaviour:** executes exactly the requested version. No per-request override of model or
  parameters is required, and none should be possible.
- **Acceptance:** an execution of an exact version can be started with an input.

### EXE-02 · Correlation context · SHOULD

- **Purpose:** trace platform cost and failures back to the workflow run and node that caused them.
- **Inputs:** workflow id and version, workflow run id, node occurrence identifier, attempt number.
- **Required behaviour:** retained on the execution and visible in traces and usage records.
- **Acceptance:** caller-supplied metadata can be attached to an execution. Querying by it is covered
  separately by REC-03.

### EXE-03 · Idempotent start · MUST · ★

- **Purpose:** a job redelivered after a worker crash must attach to the existing execution rather than
  start a second, billable one.
- **Inputs:** a caller-chosen idempotency key or client-supplied execution id, unique per node occurrence and
  attempt.
- **Required behaviour:**
  - a repeated request with the same key returns, or attaches to, the existing execution;
  - the same key with a different input is rejected with a distinct error;
  - keys are tenant-scoped;
  - the key retention window is documented.
- **Acceptance:** the platform deduplicates on a caller-supplied key or id. Without it, the window between
  sending a start request and learning its execution id cannot be closed by the platform (section 18).

### EXE-04 · Early execution identifier · MUST · ★

- **Purpose:** the SOAR records the platform's execution id before waiting for a result, so it can recover
  or cancel if its worker crashes.
- **Outputs:** the execution id, available immediately — in response headers or the first stream event —
  well before the terminal event.
- **Acceptance:** the id is available at or near the start of the stream. An id delivered only in the
  terminal event is partial coverage.

### EXE-05 · Input rejected before execution · SHOULD

- **Purpose:** invalid input fails quickly, deterministically, and without cost.
- **Required behaviour:** input that fails the input schema is rejected before any execution is created or
  billed, with field-level details (ERR-05).
- **Acceptance:** validation failures are reported before execution with a distinct error.

### EXE-06 · Admission rejection · SHOULD

- **Purpose:** distinguish "not started, try later" from "started and failed".
- **Required behaviour:** quota, capacity, or throttling refusals occur before an execution exists and
  include a retry-after hint.
- **Acceptance:** admission refusals are distinguishable from execution failures.

### EXE-07 · Unknown or disabled version rejection · MUST

- **Purpose:** a broken pin is reported clearly and never retried.
- **Required behaviour:** distinct errors for an unknown version and a disabled version, both
  non-retryable.
- **Acceptance:** both cases have documented errors.

## 8. Result streaming (STR)

### STR-01 · Results over SSE · MUST

- **Purpose:** receive an execution's progress and result.
- **Required behaviour:** a defined way to open an execution's event stream, either on the start request or
  through a separate subscribe operation.
- **Acceptance:** stream establishment is specified.

### STR-02 · Event identity · SHOULD

- **Purpose:** resume a dropped stream without skipping or duplicating events (REC-01).
- **Outputs:** an id on each event, ordered within an execution.
- **Acceptance:** events carry ids.

### STR-03 · Typed events · MUST

- **Purpose:** distinguish lifecycle changes, partial output, errors, and completion.
- **Outputs:** distinct event types with documented payloads, covering at least the events marked MUST in
  section 17.5.
- **Acceptance:** the event catalog covers those types with payload definitions.

### STR-04 · Terminal event carries the complete result · MUST

- **Purpose:** a consumer that missed intermediate events still receives the full result.
- **Outputs in the terminal event:** final status; on success, the complete output; on failure, the
  structured error; plus usage (OBS-01), trace id (OBS-02), and duration (OBS-03).
- **Acceptance:** the terminal event is self-sufficient. A result that must be assembled from partial events
  is partial coverage, and the assembly rule must be specified.

### STR-05 · Partial output events · MAY

- **Purpose:** show progress to a human.
- **Required behaviour:** display-only. The SOAR never binds downstream nodes to partial output.
- **Acceptance:** incremental output or progress events exist.

### STR-06 · Heartbeats · SHOULD

- **Purpose:** keep idle connections open through proxies and load balancers, and detect dead streams.
- **Outputs:** keep-alive events or comments at a documented interval.
- **Acceptance:** heartbeat behaviour and interval are documented.

### STR-07 · Errors after the stream opens · MUST

- **Purpose:** once a stream has started, its HTTP status has already been sent and cannot report failure.
- **Required behaviour:** failures are delivered as error events carrying the structured error (section
  17.3), followed by stream closure or a terminal event.
- **Acceptance:** mid-stream error delivery is documented.

### STR-08 · Errors before the stream opens · MUST

- **Purpose:** rejections such as authentication, not found, invalid input, or admission occur before any
  stream exists.
- **Required behaviour:** an HTTP error status with a structured error body.
- **Acceptance:** pre-stream error responses are documented.

### STR-09 · Stream completion · MUST

- **Purpose:** the SOAR knows when a stream finished normally and when a closed connection is an
  interruption.
- **Required behaviour:** the platform closes the stream after the terminal event. Any other closure is an
  interruption, not a result.
- **Acceptance:** completion and closure semantics are documented.

## 9. Recovery (REC)

### REC-01 · Reattach to an execution's stream · MUST · ★

- **Purpose:** after a dropped connection or worker crash, continue receiving an execution's events instead
  of losing its result.
- **Inputs:** execution id; ideally the last event id received.
- **Required behaviour:** replays events after the given event id, or at minimum delivers the terminal event
  of an execution that has already finished.
- **Acceptance:** a stream can be re-opened for an existing execution. Re-opening without resume is partial
  coverage.

### REC-02 · Get an execution's state and result · MUST · ★

- **Purpose:** reconcile executions the SOAR lost track of, and recover a result without a stream.
- **Inputs:** execution id.
- **Outputs:** status; when terminal, output or error, usage, and trace id; when in flight, current status.
- **Acceptance:** a non-streaming lookup by execution id exists.

### REC-03 · Find executions by key or correlation · SHOULD · ★

- **Purpose:** after a crash between sending a start request and recording its execution id, determine
  whether an execution was created.
- **Inputs:** the idempotency key, or correlation metadata (EXE-02).
- **Outputs:** matching executions with their ids and status.
- **Acceptance:** executions are queryable by a caller-supplied key or metadata. This becomes critical when
  EXE-03 is not satisfied.

### REC-04 · Result retention window · MUST

- **Purpose:** know how long after completion an execution's result remains recoverable.
- **Acceptance:** the retention period for execution state and results is documented.

## 10. Cancellation (CAN)

### CAN-01 · Explicit cancel · MUST · ★

- **Purpose:** stop an execution when its workflow run is cancelled or the SOAR's deadline expires, and stop
  the associated cost.
- **Inputs:** execution id.
- **Required behaviour:** idempotent. Cancelling a finished execution returns its final state unchanged.
- **Acceptance:** cancellation by execution id exists.

### CAN-02 · Disconnect semantics · MUST · ★

- **Purpose:** know whether a dropped or closed stream leaves an execution running and billing with no
  consumer.
- **Required behaviour:** the platform states whether closing a stream cancels the execution or lets it
  continue.
- **Acceptance:** the behaviour is documented. Where it is not, section 18 requires assuming the execution
  continues.

### CAN-03 · Cancelled is a distinct terminal state · MUST

- **Purpose:** an intentional stop must not be treated as an agent failure or routed to error handling.
- **Acceptance:** cancellation has its own status or event, distinct from failure.

## 11. Timeouts (TMO)

### TMO-01 · Caller-set timeout · MUST

- **Purpose:** the node's workflow context determines how long an execution may run.
- **Inputs:** a timeout per execution, within the contract maximum (CON-04).
- **Required behaviour:** enforced by the platform.
- **Acceptance:** a per-execution timeout exists and is enforced.

### TMO-02 · Timeout outcome · MUST

- **Purpose:** distinguish a timeout from other failures, and know whether it can be retried.
- **Required behaviour:** a distinct timeout error with retryability stated (ERR-01); the execution stops
  and billing ends.
- **Acceptance:** the timeout outcome is documented.

## 12. Errors (ERR)

### ERR-01 · Explicit retryability · SHOULD

- **Purpose:** only the platform knows whether it already exhausted its internal retries; retryability must
  not be guessed from HTTP status codes.
- **Outputs:** a retryable indicator, or equivalent, on every error.
- **Acceptance:** each error states retryability. Retryability that can only be inferred from codes is
  partial coverage.

### ERR-02 · Retry-after hint · SHOULD

- **Outputs:** a minimum wait before retrying, on throttling and transient errors, in the body or a header.
- **Acceptance:** a retry-after value is provided where applicable.

### ERR-03 · Rejection versus execution failure · MUST

- **Purpose:** determine whether an execution exists — which decides whether retrying is safe and whether
  anything needs cancelling.
- **Acceptance:** every error makes it unambiguous whether no execution was created or an execution exists
  and failed.

### ERR-04 · Error catalogue · MUST

- **Outputs:** a documented error code, or equivalent, for every error class in section 17.4.
- **Acceptance:** each class maps to a documented error (table 20.3).

### ERR-05 · Field-level details · SHOULD

- **Purpose:** point an author at the exact input field that is wrong.
- **Outputs:** for each problem, a location such as a field path or pointer, and a message.
- **Acceptance:** validation errors carry per-field locations.

### ERR-06 · Internal retry behaviour · SHOULD

- **Purpose:** avoid multiplying retries across layers — for example the platform retrying three times
  inside each of three SOAR attempts.
- **Outputs:** whether the platform retries transient provider failures internally, and how often.
- **Acceptance:** internal retry behaviour is documented.

## 13. Output (OUT)

### OUT-01 · Output conforms to the output schema · SHOULD

- **Purpose:** downstream nodes bind to output fields at design time; malformed output breaks them.
- **Required behaviour:** the platform guarantees conformance — for example through structured output and
  repair — or explicitly states that it does not. A violation is reported as a distinct error.
- **Acceptance:** the conformance guarantee, or its absence, is stated. The SOAR re-validates output in
  either case.

### OUT-02 · Empty and null output · MAY

- **Acceptance:** the platform defines whether a successful execution may return null or empty output.

## 14. Observability and cost (OBS)

### OBS-01 · Usage per execution · MUST

- **Purpose:** attribute cost to the workflow run and node that caused it.
- **Outputs:** tokens or other billable units, and cost where available.
- **Acceptance:** usage is reported per execution.

### OBS-02 · Trace identifier · SHOULD

- **Purpose:** link a failed node directly to the platform's trace for investigation.
- **Outputs:** a trace id; optionally a link to the platform's trace view.
- **Acceptance:** a trace id is returned per execution.

### OBS-03 · Duration · SHOULD

- **Purpose:** separate agent run time from queueing and waiting on the SOAR side.
- **Outputs:** execution duration, or start and end timestamps.
- **Acceptance:** duration or timestamps are reported per execution.

### OBS-04 · Intermediate steps · MAY

- **Purpose:** show model calls, tool calls, and output repairs when investigating one execution.
- **Acceptance:** execution steps are retrievable.

## 15. Data handling (DAT)

### DAT-01 · Size limits · MUST

- **Purpose:** agent inputs often carry large security artefacts such as alerts and log excerpts.
- **Outputs:** maximum input and output sizes, and the behaviour when exceeded.
- **Acceptance:** size limits are documented.

### DAT-02 · Platform retention of inputs and outputs · MUST

- **Purpose:** inputs may contain sensitive data such as identities, hostnames, and credentials in log
  excerpts.
- **Outputs:** how long the platform retains inputs, outputs, and traces; whether retention is configurable
  per tenant.
- **Acceptance:** retention is documented. Per-tenant configurability is desirable but not required.

### DAT-03 · Sensitive data in logs and traces · SHOULD

- **Outputs:** how the platform handles sensitive values in its logs and traces, and any redaction options.
- **Acceptance:** handling is documented.

## 16. Lifecycle and event subscriptions (EVT)

### EVT-01 · Agent version lifecycle notifications · MAY

- **Purpose:** invalidate the SOAR's catalog cache, and flag workflows pinned to a version that has just
  been deprecated or disabled.
- **Outputs:** notification when a version is published, deprecated, or disabled — for example through a
  subscribable event stream.
- **Acceptance:** lifecycle notifications are available.

### EVT-02 · Tenant-wide execution event stream · MAY

- **Purpose:** a single long-lived subscriber receives terminal events for many executions, so workers need
  not hold one stream per execution while agents run.
- **Outputs:** a subscription to all execution events for a tenant, with a durable, resumable cursor and a
  documented retention window.
- **Acceptance:** such a stream exists. Without a resumable cursor, coverage is partial.

## 17. Logical data model

Logical names used throughout this specification. Platform field names are recorded against them in
section 20.

### 17.1 Contract fields

| Logical field | Type | Level | Description | Requirements |
|---|---|---|---|---|
| `agent_id` | string | MUST | Stable agent identifier | CAT-01, CON-01 |
| `agent_name` | string | MUST | Display name | CAT-01 |
| `agent_description` | string | SHOULD | Purpose of the agent | CAT-01 |
| `agent_tags` | list of strings | MAY | Classification tags | CAT-01 |
| `agent_owner` | string | MAY | Maintaining team or user | CAT-01 |
| `latest_published_version` | version | MUST | Newest usable version | CAT-01, CAT-02 |
| `version` | version | MUST | Exact, orderable version identifier | CAT-03, CON-01 |
| `lifecycle_status` | usable, deprecated, disabled | MUST | Whether the version may be pinned and executed | CON-06 |
| `deprecation_replacement` | version | MAY | Suggested replacement version | CON-06 |
| `deprecation_message` | string | MAY | Explanation shown to authors | CON-06 |
| `published_at` | timestamp | SHOULD | When the version was published | CAT-03, CON-01 |
| `input_schema` | schema | MUST | Shape of an execution's input | CON-01 |
| `output_schema` | schema | MUST | Shape of an execution's output | CON-01 |
| `schema_dialect` | string | MUST | Schema language and version | CON-03 |
| `default_timeout` | duration | SHOULD | Pre-filled node timeout | CON-04 |
| `max_timeout` | duration | SHOULD | Upper bound for a node timeout | CON-04 |
| `recommended_attempts` | integer | MAY | Suggested retry attempts | CON-04 |
| `contract_fingerprint` | string | SHOULD | Digest of the fields bindings depend on | CON-05 |
| `side_effect_class` | read-only, action-taking | SHOULD | Whether executions cause external effects | CON-07 |
| `model_provider` | string | MAY | Model provider | CON-08 |
| `model_name` | string | MAY | Model identifier | CON-08 |
| `model_parameters` | object | MAY | Key parameters, informational | CON-08 |

### 17.2 Execution fields

| Logical field | Type | Level | Description | Requirements |
|---|---|---|---|---|
| `execution_id` | string | MUST | Platform identifier of the execution | EXE-04, REC-02, CAN-01 |
| `idempotency_key` | string | MUST | Caller-chosen deduplication key | EXE-03, REC-03 |
| `agent_id` | string | MUST | Agent executed | EXE-01 |
| `version` | version | MUST | Exact version executed | EXE-01 |
| `input` | object | MUST | Execution input | EXE-01 |
| `timeout` | duration | MUST | Caller-set execution timeout | TMO-01 |
| `correlation.workflow_id` | string | SHOULD | Workflow identifier | EXE-02 |
| `correlation.workflow_version` | string | SHOULD | Published workflow version | EXE-02 |
| `correlation.workflow_run_id` | string | SHOULD | Workflow run identifier | EXE-02 |
| `correlation.node_occurrence` | string | SHOULD | Node occurrence, including loop iteration and nesting | EXE-02 |
| `correlation.attempt` | integer | SHOULD | Deliberate retry number | EXE-02 |
| `status` | started, running, succeeded, failed, cancelled | MUST | Execution state | STR-03, REC-02, CAN-03 |
| `output` | object | MUST | Final output on success | STR-04, REC-02 |
| `error` | error (17.3) | MUST | Structured error on failure | STR-04, STR-07, REC-02 |
| `usage.input_units` | number | MUST | Input tokens or billable units | OBS-01 |
| `usage.output_units` | number | MUST | Output tokens or billable units | OBS-01 |
| `usage.cost` | number | SHOULD | Cost, where available | OBS-01 |
| `trace_id` | string | SHOULD | Platform trace identifier | OBS-02 |
| `trace_link` | URL | MAY | Link to the platform trace view | OBS-02 |
| `started_at` | timestamp | SHOULD | Execution start | OBS-03 |
| `finished_at` | timestamp | SHOULD | Execution end | OBS-03 |
| `duration` | duration | SHOULD | Execution run time | OBS-03 |
| `event_id` | string | SHOULD | Identifier of a stream event | STR-02, REC-01 |

### 17.3 Error fields

| Logical field | Type | Level | Description | Requirements |
|---|---|---|---|---|
| `code` | string | MUST | Machine-readable error code | ERR-04 |
| `message` | string | MUST | Human-readable explanation | ERR-04 |
| `retryable` | boolean | SHOULD | Whether retrying can succeed | ERR-01 |
| `retry_after` | duration | SHOULD | Minimum wait before retrying | ERR-02 |
| `details[].location` | string | SHOULD | Field path or pointer to the problem | ERR-05 |
| `details[].message` | string | SHOULD | Explanation for that field | ERR-05 |
| `execution_exists` | boolean, derivable | MUST | Whether an execution was created | ERR-03 |

### 17.4 Error classes

The expected classification is shown for reference. Where the platform provides an explicit retryability
signal, that signal takes precedence.

| Error class | When it occurs | Expected retryability | Execution exists? |
|---|---|---|---|
| `authentication_failed` | Invalid or missing credential | No | No |
| `forbidden` | Credential lacks permission | No | No |
| `agent_not_found` | Unknown or invisible agent | No | No |
| `version_not_found` | Unknown or invisible version | No | No |
| `execution_not_found` | Unknown or invisible execution | No | — |
| `version_disabled` | Execution of a disabled version | No | No |
| `input_invalid` | Input fails the input schema | No | No |
| `idempotency_conflict` | Same key reused with a different input | No | No new execution |
| `throttled_or_quota` | Rate limit or quota exceeded | Yes, after retry-after | No |
| `provider_unavailable` | Model provider unavailable after internal retries | Yes | Either |
| `timeout` | Execution exceeded its timeout | Yes | Yes |
| `output_schema_violation` | Output does not conform to the output schema | No | Yes |
| `internal` | Unexpected platform error | Yes | Either |

### 17.5 Event types

| Logical event | Level | Terminal | Payload | Requirements |
|---|---|---|---|---|
| `execution_started` | MUST | No | `execution_id`, `status` | STR-03, EXE-04 |
| `status_changed` | MAY | No | `execution_id`, `status` | STR-03 |
| `output_delta` | MAY | No | Partial output | STR-05 |
| `step` | MAY | No | Intermediate step summary | OBS-04 |
| `heartbeat` | SHOULD | No | None | STR-06 |
| `error` | MUST | Sometimes | Structured error (17.3) | STR-07 |
| `execution_succeeded` | MUST | Yes | Complete output, usage, trace id, duration | STR-04 |
| `execution_failed` | MUST | Yes | Structured error, usage, trace id, duration | STR-04 |
| `execution_cancelled` | MUST | Yes | `execution_id`, usage so far | CAN-03 |

## 18. Correctness criteria

These criteria define the reliability the integration achieves, based on coverage of the ★ requirements.

### C-1 · Avoiding duplicate executions on retry

A redelivered job can avoid starting a duplicate execution when:

- **EXE-03 is satisfied** — the job repeats the start request with the same idempotency key; or
- **EXE-03 is not satisfied but REC-03 is** — the job looks up an execution by key before starting a new
  one.

When neither holds, the SOAR cannot determine whether an execution started if a crash occurred between
sending the start request and recording the execution id. In that case the SOAR applies this policy:

| Side-effect class (CON-07) | Behaviour on an ambiguous outcome |
|---|---|
| Read-only | Retry. The worst case is a duplicate, billable execution. |
| Action-taking | Do not retry. Fail with an unknown-outcome status for a human to resolve. |
| Undeclared | Treated as action-taking. |

### C-2 · Recovering a result after a dropped stream

A result can be recovered after a dropped connection or worker crash when **REC-01 or REC-02 is satisfied**
and **EXE-04 is at least partially satisfied**. Otherwise a dropped stream loses the result, and the
execution is treated as an ambiguous outcome under C-1.

### C-3 · Stopping orphaned executions

Executions abandoned by crashed workers can be stopped when **CAN-01 is satisfied**.

Where CAN-02 is undocumented, the integration assumes that closing a stream does **not** cancel the
execution. If, in addition, CAN-01 is not satisfied, abandoned executions continue to run and incur cost
with no means of stopping them; this must be recorded as a risk.

## 19. SOAR responsibilities

These are performed by the SOAR, not the platform. They are listed to show how platform gaps are
compensated for, and are not mapped to platform endpoints.

| Responsibility | Relies on |
|---|---|
| Persist an intent record — idempotency key, request hash, status, and later the execution id — **before** starting an execution, and consult it when a job is retried | EXE-03, EXE-04, REC-03 |
| Compose the idempotency key from the workflow run, the node occurrence including loop iteration and nesting, and the attempt number | EXE-03 |
| Validate resolved input against the pinned input schema before starting an execution | CON-01, CON-03 |
| Re-validate output against the pinned output schema before recording the node result | CON-01, OUT-01 |
| Pin exact versions, store a contract snapshot and fingerprint on each node, and re-fetch the contract at publish | CON-01, CON-02, CON-05, CON-06 |
| Retry only retryable terminal failures, with backoff, respecting the platform's internal retries | ERR-01, ERR-02, ERR-06 |
| Reconcile stuck and orphaned executions | REC-02, CAN-01 |
| Hold platform credentials server-side and derive the tenant server-side | GEN-01, GEN-02 |
| Apply the ambiguous-outcome policy by side-effect class | CON-07, section 18 |

## 20. Traceability matrix

Record the platform capability that satisfies each requirement.

**Coverage values**

| Value | Meaning |
|---|---|
| **Full** | Every required input, output, and behaviour is present. |
| **Partial** | Some are missing; list which in Notes. |
| **None** | Nothing in the platform addresses the requirement. |
| **Undocumented** | A capability exists, but the required behaviour is not stated. |

### 20.1 Requirement mapping

| ID | Requirement | Level | Platform endpoint(s) | Fields / events | Coverage | Notes |
|---|---|---|---|---|---|---|
| GEN-01 | Service authentication | MUST | | | | |
| GEN-02 | Tenant scoping | MUST | | | | |
| GEN-03 | API versioning and compatibility | SHOULD | | | | |
| GEN-04 | Rate-limit signalling | SHOULD | | | | |
| CAT-01 | List agents | MUST | | | | |
| CAT-02 | Get one agent | MAY | | | | |
| CAT-03 | List an agent's versions | MUST | | | | |
| CON-01 | Get a version's contract | MUST | | | | |
| CON-02 | Immutable published versions | MUST | | | | |
| CON-03 | Declared schema dialect | MUST | | | | |
| CON-04 | Execution limits | SHOULD | | | | |
| CON-05 | Contract fingerprint | SHOULD | | | | |
| CON-06 | Lifecycle status | MUST | | | | |
| CON-07 | Side-effect declaration | SHOULD | | | | |
| CON-08 | Model configuration visibility | MAY | | | | |
| EXE-01 | Start an execution of a pinned version | MUST | | | | |
| EXE-02 | Correlation context | SHOULD | | | | |
| EXE-03 | Idempotent start ★ | MUST | | | | |
| EXE-04 | Early execution identifier ★ | MUST | | | | |
| EXE-05 | Input rejected before execution | SHOULD | | | | |
| EXE-06 | Admission rejection | SHOULD | | | | |
| EXE-07 | Unknown or disabled version rejection | MUST | | | | |
| STR-01 | Results over SSE | MUST | | | | |
| STR-02 | Event identity | SHOULD | | | | |
| STR-03 | Typed events | MUST | | | | |
| STR-04 | Terminal event carries the complete result | MUST | | | | |
| STR-05 | Partial output events | MAY | | | | |
| STR-06 | Heartbeats | SHOULD | | | | |
| STR-07 | Errors after the stream opens | MUST | | | | |
| STR-08 | Errors before the stream opens | MUST | | | | |
| STR-09 | Stream completion | MUST | | | | |
| REC-01 | Reattach to an execution's stream ★ | MUST | | | | |
| REC-02 | Get an execution's state and result ★ | MUST | | | | |
| REC-03 | Find executions by key or correlation ★ | SHOULD | | | | |
| REC-04 | Result retention window | MUST | | | | |
| CAN-01 | Explicit cancel ★ | MUST | | | | |
| CAN-02 | Disconnect semantics ★ | MUST | | | | |
| CAN-03 | Cancelled is a distinct terminal state | MUST | | | | |
| TMO-01 | Caller-set timeout | MUST | | | | |
| TMO-02 | Timeout outcome | MUST | | | | |
| ERR-01 | Explicit retryability | SHOULD | | | | |
| ERR-02 | Retry-after hint | SHOULD | | | | |
| ERR-03 | Rejection versus execution failure | MUST | | | | |
| ERR-04 | Error catalogue | MUST | | | | |
| ERR-05 | Field-level details | SHOULD | | | | |
| ERR-06 | Internal retry behaviour | SHOULD | | | | |
| OUT-01 | Output conforms to the output schema | SHOULD | | | | |
| OUT-02 | Empty and null output | MAY | | | | |
| OBS-01 | Usage per execution | MUST | | | | |
| OBS-02 | Trace identifier | SHOULD | | | | |
| OBS-03 | Duration | SHOULD | | | | |
| OBS-04 | Intermediate steps | MAY | | | | |
| DAT-01 | Size limits | MUST | | | | |
| DAT-02 | Platform retention of inputs and outputs | MUST | | | | |
| DAT-03 | Sensitive data in logs and traces | SHOULD | | | | |
| EVT-01 | Agent version lifecycle notifications | MAY | | | | |
| EVT-02 | Tenant-wide execution event stream | MAY | | | | |

### 20.2 Event mapping

| Logical event (17.5) | Level | Platform event name | Platform payload fields | Coverage | Notes |
|---|---|---|---|---|---|
| `execution_started` | MUST | | | | |
| `status_changed` | MAY | | | | |
| `output_delta` | MAY | | | | |
| `step` | MAY | | | | |
| `heartbeat` | SHOULD | | | | |
| `error` | MUST | | | | |
| `execution_succeeded` | MUST | | | | |
| `execution_failed` | MUST | | | | |
| `execution_cancelled` | MUST | | | | |

### 20.3 Error mapping

| Error class (17.4) | Platform code or HTTP status | Before or during stream | Retryability signal | Retry-after source | Coverage |
|---|---|---|---|---|---|
| `authentication_failed` | | | | | |
| `forbidden` | | | | | |
| `agent_not_found` | | | | | |
| `version_not_found` | | | | | |
| `execution_not_found` | | | | | |
| `version_disabled` | | | | | |
| `input_invalid` | | | | | |
| `idempotency_conflict` | | | | | |
| `throttled_or_quota` | | | | | |
| `provider_unavailable` | | | | | |
| `timeout` | | | | | |
| `output_schema_violation` | | | | | |
| `internal` | | | | | |

### 20.4 Coverage summary

| Level | Total | Full | Partial | None | Undocumented |
|---|---|---|---|---|---|
| MUST | 31 | | | | |
| SHOULD | 19 | | | | |
| MAY | 7 | | | | |
| **★ critical** | **7** | | | | |

### 20.5 Correctness outcome

| Criterion | Outcome | Basis |
|---|---|---|
| C-1 · Duplicate executions avoidable on retry | | |
| C-2 · Result recoverable after a dropped stream | | |
| C-3 · Orphaned executions stoppable | | |
