# Planning prompt: SOAR ↔ AI agent platform integration

**How to use:** copy everything below the line into the other system. Fill in `<api_spec>` (required) and
`<soar_notes>` (optional). Nothing else needs editing.

The prompt produces a detailed **plan** — decisions, approach, sequencing, and verification — and
explicitly no implementation: no code, SQL, configuration, or pseudo-code.

If you can, include one or two raw SSE transcripts of real executions alongside the spec — a success and a
failure — and one real agent contract with its input and output schemas. API docs often omit what the
terminal event actually contains and which schema keywords agents really use, and the plan depends on both.

---

<api_spec>
[PASTE THE AI AGENT PLATFORM'S API SPECIFICATION HERE.
Include: authentication, every endpoint, request and response schemas, SSE event names and payloads,
error formats, rate-limit behaviour, the agent contract format (input schema, output schema, limits, model
configuration), and any versioning or lifecycle rules for agents. OpenAPI, prose docs, or example requests
with raw streamed responses are all fine.]
</api_spec>

<soar_notes>
[OPTIONAL. Anything you can share about the SOAR: workflow engine, job queue and its delivery guarantees,
database, deployment topology, existing node types, how bindings between nodes are expressed, how node
results are checkpointed. Leave this empty if none can be shared; the plan must then state its
assumptions explicitly.]
</soar_notes>

You are a senior distributed-systems architect acting as technical lead. Produce a **detailed plan** for
integrating a SOAR (security orchestration, automation, and response) workflow engine with the AI agent
platform described in `<api_spec>`, so that workflow authors can find agents, add them to workflows, bind
their inputs and outputs, configure them, and run them reliably in production.

The plan must let an engineering team understand what to build, which decisions to make first, in what
order to deliver, and how to prove each stage works — without you building any of it. Correctness under
failure matters more than elegance: this system can trigger security actions, so a duplicated or silently
lost agent execution is a real incident, not a cosmetic bug.

## Ground rules

1. **Plan, do not implement.** Do not write code, SQL or DDL, configuration, pseudo-code, JSON schemas, or
   example payloads. Where a precise shape matters — a persisted record, an error object — describe it as a
   table of field, purpose, and constraint. Describe algorithms and behaviour in prose and decision tables.
2. **The spec is the source of truth.** Every endpoint, header, field, event, or error code the plan relies
   on must exist in `<api_spec>`. Cite it by operation, path, or field name. Do not invent platform
   capabilities, and do not assume a polling or status endpoint exists unless the spec shows one.
3. **Never fill a gap silently.** When the spec is silent on something the plan depends on, write
   `ASSUMPTION:` with the safest interpretation, plan for that interpretation, and list it under open
   questions.
4. **Separate what the SOAR builds from what you would ask the platform to change.** Platform asks go in
   their own section. The plan must work without them unless you state otherwise and explain why.
5. **Prefer mechanisms the SOAR controls** over ones that need platform changes.
6. **Explain the why** behind each significant decision, including the specific failure it prevents.
7. **No component, endpoint, or task without a reason.** Every task traces to a requirement below; if
   nothing needs it, leave it out.
8. **No calendar dates.** The team's capacity is unknown. Sequence work, show dependencies, and size tasks
   relatively (S / M / L) instead.

## System context

- **SOAR workflow engine.** Workflows are graphs of nodes. Data flows between nodes through bindings: a node
  input is bound to an upstream node's output field, a literal, or a workflow variable. A workflow is
  published as an immutable version, and runs execute that version. Nodes run as jobs pulled by workers
  from a durable queue.
- **Delivery is at-least-once** unless `<soar_notes>` says otherwise: a worker can crash mid-job and the job
  is redelivered to another worker. Plan for that.
- **Workflow designer.** A browser UI served by the SOAR backend, where authors build workflows.
- **AI agent platform.** Hosts agents. Each agent version has a contract: an input schema, an output schema,
  execution limits, and model configuration. Execution results are delivered as a Server-Sent Events (SSE)
  stream.

## Requirements the plan must satisfy

### Product

1. Authors can find agents in the designer, add them as nodes, bind inputs and outputs like any other typed
   action, configure execution settings, and publish the workflow.
2. Agents are tested in the platform's own tooling; the designer does not need its own test run. Account for
   the trade-off: authors cannot rehearse an agent against real upstream node output before publishing, so
   design-time and publish-time checks on bindings carry more weight.

### Architecture decisions already made

Keep these unless the spec makes one impossible. If it does, say so and propose the closest alternative.

1. **The browser never calls the platform.** All platform calls go through the SOAR backend using a service
   credential. The tenant is derived server-side and never accepted from browser input.
2. **Nodes pin an exact agent version.** Treat published versions as immutable. Floating on "latest" is
   rejected: it breaks bindings silently and makes runs non-reproducible.
3. **Contract snapshot plus fingerprint.** When an agent is added, the node stores a snapshot of the pinned
   version's input schema, output schema, and limits, plus a digest. At publish, the backend re-fetches the
   contract from the platform and verifies status and digest — the stored snapshot is never trusted at
   publish.
   - If the spec provides no digest, plan one: for example SHA-256 over RFC 8785 canonical JSON of exactly
     the fields bindings and settings depend on. State what it includes and deliberately excludes, and why.
4. **Model configuration belongs to the agent version** and is shown read-only. No per-node overrides of
   model or temperature, because they would make the same pinned version behave differently in different
   workflows.
5. **Execution settings belong to the node:** timeout, maximum attempts, backoff, and on-error behaviour —
   `fail` (the run fails), `continue` (the node output is null and the run proceeds), or `route` (an error
   branch receives a structured error).

### Agent contract: inputs and outputs

The contract is what lets an agent behave like any other typed action in a workflow. The plan must address
all of the following.

**1. Contract inventory**

Map the platform's contract to what the SOAR needs, and flag anything missing:

- identity, version, and lifecycle status;
- input schema and output schema, and the schema dialect they use;
- limits — default timeout, maximum timeout, recommended attempts — or SOAR-side defaults if the spec has
  none;
- model configuration, informational only;
- a digest or equivalent fingerprint;
- a side-effect class (read-only versus action-taking), if the platform declares one. If it does not, state
  where the SOAR records it and who is allowed to set it.

**2. Schema dialect and validator compatibility**

- Identify the schema dialect and the keywords agent schemas actually use.
- Establish whether the SOAR's validator supports them. Where the designer validates with one library and
  the platform validates with another, the two can disagree on edge cases — `$ref` resolution, `format`
  assertions, `unevaluatedProperties`, conditional subschemas. Decide which keywords are supported, what
  happens when a contract uses an unsupported one, and which side is authoritative when they disagree.

**3. Input binding and validation**

- **Binding model.** How each input field is bound: from an upstream node's output field, a literal, or a
  workflow variable; how nested objects and arrays are handled; how required fields with no binding are
  reported. Reuse the SOAR's existing binding approach rather than introducing a new one.
- **Design time.** Bindings are type-checked against the input schema and against the upstream node's
  output schema — types, required fields, enums, formats — with mismatches reported on the specific input.
- **Publish time.** The check is re-run against the re-fetched contract, since the snapshot may be stale.
- **Runtime.** Bindings are resolved, then the resolved input is validated against the pinned input schema
  *before* the platform is called. A failure is non-retryable, costs nothing, and must not leave an intent
  record that looks like a started execution.

**4. Output exposure and validation**

- **Downstream bindings.** How the output becomes available to later nodes as typed fields derived from the
  output schema, consistent with how other actions expose outputs.
- **Conformance.** Determine whether the platform guarantees output conforms to the output schema. Either
  way, output is re-validated against the pinned output schema before the node result is recorded, and a
  violation is non-retryable. If there is no guarantee, state what that costs.
- **Streaming.** Partial output from the stream is display-only and never bindable. Only the validated final
  result becomes the node's output.
- **`continue`.** The node output is null. The publish validator warns when a downstream required input binds
  to a node configured to continue on error.
- **`route`.** Specify the fields the error branch receives — at least code, message, retryable, attempts,
  and execution and trace identifiers — as a table, so error-branch nodes can bind to them like any other
  output.

**5. Contract changes and upgrades**

- Existing published workflow versions keep running on their pinned version; upgrading is an explicit author
  action.
- Every contract difference is classified. Breaking: an output field that something binds to is removed,
  renamed, or changes type; a new required input; a narrowed type or enum; a maximum timeout lowered below a
  node's configured value. Non-breaking: a new optional input; a new output field; a widened type.
- The upgrade flow shows each difference and highlights exactly which bindings and settings break.

**6. Data handling**

Agent inputs are likely to carry sensitive security data — alerts, identities, hostnames, credentials in
log excerpts.

- What leaves the SOAR, and whether any fields need redaction or minimisation before the call.
- What the intent log retains. A canonical request hash is always required. Retaining the full payload is a
  retention and exposure decision: make it explicitly, with retention periods.
- Inputs and outputs stay out of general application logs; state where they are kept and for how long.
- Input and output size limits, and what happens when they are exceeded.

### Execution reliability — the most important part

**1. Idempotency identity**

- The key is `run_id + node_path + attempt`.
- `node_path` must identify the node *occurrence*, not just the node: loop iteration and sub-workflow
  nesting must be part of it. Otherwise every iteration of a loop produces the same key and silently reuses
  the first iteration's result, with no error raised.
- `attempt` increments only for a deliberate retry after a terminal failure. A redelivered job reuses the
  same key.

**2. Intent log**

Before the platform is called, the key and request are persisted in a database; the platform's run
identifier is recorded as soon as it is known; the terminal result is recorded at the end. The plan must
cover:

- **Records and fields**, as tables of field, purpose, and constraint: a unique constraint on the key; a
  hash of the request over canonical JSON (not raw bytes — re-serialization changes key order and
  whitespace); status; platform run identifier; owner, lease expiry, and heartbeat; timestamps; result,
  usage, and error.
- **States and transitions**, stating which step performs each transition and when it commits relative to
  the network call. The intent record must be committed *before* the platform is called.
- **Retry-time decision table**: what a redelivered or retried job does for each state it finds.
- **The ambiguous window.** A crash after the request was sent but before the platform's run identifier was
  recorded leaves the database unable to tell whether a run started. Determine from the spec whether this
  window can be closed — a client-supplied run id or idempotency key the platform deduplicates on, or runs
  queryable by metadata, tag, or external id. If it cannot be closed, say so plainly.
- **Policy for the ambiguous case, by side-effect class.** Read-only agents (triage, enrichment,
  summarisation) may retry; the worst case is paying twice. Action-taking agents (blocking, disabling,
  isolating) must not: they fail with a distinct `UNKNOWN_OUTCOME` routed to an analyst. Use the side-effect
  class from the contract inventory, and state the default when it is not declared.
- **Reconciliation.** A sweeper for records stuck in pending or running states past their deadline; lease
  takeover for records owned by dead workers; detection and cancellation of orphaned platform runs that may
  still be billing.

**3. Streaming**

- Requirements for the stream consumer: which SSE elements it must handle (event id, event type, data,
  heartbeat comments), idle timeouts, and buffering by proxies and load balancers.
- Which event is terminal, and whether it carries the complete result. If only incremental deltas exist,
  how the final result is assembled and validated against the output schema.
- **A dropped connection is not a failed execution.** Plan recovery from what the spec offers — reconnecting
  with `Last-Event-ID`, re-subscribing by run id, or fetching final state — or state the limitation and its
  consequence.
- Errors that arrive mid-stream as events, versus HTTP errors returned before the stream opens.
- Resource cost: a held connection per in-flight execution, and the resulting concurrency limits.
- **Evaluate a park-and-resume variant**, where the job yields and a shared listener resumes the node on the
  terminal event, freeing workers during long runs. Recommend it only if the spec supports what it needs,
  such as subscribing to a run after the fact or a durable, resumable event cursor. Otherwise explain why a
  worker must hold the stream.

**4. Timeouts and cancellation**

- A node timeout — pre-filled from the contract's default and capped by its maximum — sent to or enforced
  against the platform, and a SOAR-side deadline slightly longer than it.
- What happens when each expires. Determine from the spec whether closing the stream stops the run and its
  billing, or whether an explicit cancel operation exists.
- Run-level cancellation propagates to in-flight executions.
- **Cancellation is not failure.** A cancelled execution must not trigger on-error routing or count against
  agent reliability.

**5. Errors and retries**

- Every platform error and failure event is classified as retryable or not. If the spec provides an explicit
  retryable signal, use it rather than inferring from status codes; justify any inference.
- A request-level rejection (nothing started, safe to retry with the same key) is distinguished from an
  execution-level failure (a run exists).
- **Retry layering.** If the platform retries transient provider errors internally, the SOAR retries only
  terminal retryable failures, with a low default maximum (for example 2), so retries do not multiply across
  layers.
- Retry-after and rate-limit signals are honoured and used to pace the job queue.

**6. Audit**

- The execution identifier, trace identifier, contract digest used, usage, and latency are retained per node
  occurrence, for audit and per-run cost attribution.

### Design time

- Which spec operations back listing, searching, filtering, version listing, and contract retrieval.
- Catalog caching: time-to-live and invalidation strategy. Published contracts are immutable, so a pinned
  version's contract can be cached far longer than the catalog list.
- The "update available" signal, driving the upgrade flow described under contract changes.
- How version lifecycle states in the spec (such as deprecated or disabled) affect pinning, execution, and
  publish.
- The complete publish-validation checklist: version status, digest match, bindings type-checked against the
  re-fetched contract, node timeout within the contract maximum, an error edge present for `route`, and
  warnings for required inputs bound to `continue` nodes.

### If the plan includes SOAR-side APIs

For example, backend endpoints that the designer calls:

- Responses tolerate added fields; requests reject unknown fields.
- Platform errors are translated rather than forwarded, so the designer can distinguish a broken pin, an
  unavailable platform, and a misconfigured credential — which is an operator problem, not an author error.
- Contracts pass through unchanged, so the snapshot and digest the designer stores match what publish will
  re-fetch.
- Describe these endpoints as a table of operation, caller, purpose, and failure modes — not as an API
  definition.

## Deliverables

Produce these sections, in this order.

1. **Executive summary** — the recommended approach in one paragraph, the guarantee it achieves, the top
   three risks, and the decisions needed before work starts.
2. **Spec digest** — a table of every operation and event the plan uses, with its purpose, auth, and key
   fields. Summarise the platform's error model and SSE event catalog.
3. **Load-bearing facts** — answer each from the spec with citations, or mark `ASSUMPTION:`
   1. Recovery after a dropped stream.
   2. Platform-side idempotency or a client-supplied run id.
   3. Lookup of runs by metadata, tag, or external id.
   4. Whether closing a stream cancels the run and its billing; any explicit cancel operation.
   5. The terminal event's shape, and whether it carries full output, usage, and trace id.
   6. Retryability signals and rate-limit headers.
   7. Versioning, immutability, and any contract digest.
   8. The tenancy and authentication model.
   9. The schema dialect of input and output schemas, and whether output conformance is guaranteed.
   10. Whether agents declare side effects, or anything equivalent.
   11. Input and output size limits.
4. **Gap analysis** — a table of requirement, what the spec provides, and the consequence for the plan.
5. **Agent contract plan** — the contract inventory mapped to SOAR needs; the dialect and validator
   decision; binding and input-validation rules at design time, publish, and runtime; output exposure,
   including the error-branch fields; a breaking-change classification table; and the data-handling
   decisions.
6. **Execution reliability plan** — the idempotency identity; intent-log records as field tables; a states
   and transitions table with commit ordering; the retry-time decision table; the error and event
   classification table; the exactly-once analysis, stating the precise guarantee achievable with this spec,
   where it breaks, and the side-effect policy covering the gap; and the streaming, timeout, and
   cancellation approach.
7. **Architecture overview** — components and their responsibilities, with a mermaid diagram.
8. **Flows** — mermaid sequence diagrams illustrating the planned behaviour:
   1. Design time: browse, pin with snapshot, bind, publish with re-verification.
   2. Runtime happy path, including input validation before the call and output validation after it.
   3. Worker crash mid-execution and retry, covering every intent-log state.
   4. Timeout, and run cancellation.
   5. Park-and-resume — only if you recommend it.
9. **Decision log** — each decision the team must make: the options, your recommendation, the rationale, the
   owning role, and the milestone it blocks. At minimum: who sets an agent's side-effect class and its
   default; full-payload retention versus hash only; the ambiguous-case policy; park-and-resume or a held
   stream; validator authority and unsupported keywords; catalog and contract cache lifetimes; approval gates
   for high-impact actions.
10. **Delivery plan** — phases in order, each with its scope, dependencies, exit criteria ("done when"), and
    relative size. Sequence to reduce risk: prefer shipping catalog and pinning before execution, and
    read-only agents before action-taking ones.
11. **Work breakdown** — workstreams (for example: catalog and contract; designer bindings and validation;
    execution and intent log; stream consumer; reconciliation and operations; security and data handling;
    verification), each with tasks that name the requirement they satisfy, their dependencies, anything that
    blocks them (a platform ask or open decision), and a relative size.
12. **Verification plan** — scenarios with expected outcomes, mapped to the phase that must pass them. At
    minimum:
    - **Execution:** success; slow success; retryable failure; non-retryable failure; timeout; run
      cancellation; a worker crash at each point (before the intent record, after the record but before the
      call, after the call but before the run id is recorded, mid-stream, and after the terminal event but
      before it is recorded); two deliveries of the same job racing; a stream dropped mid-run; a mid-stream
      error event; platform rate limiting; multiple loop iterations of the same node.
    - **Contract:** a binding type mismatch caught at design time; a contract that changed between drop and
      publish; resolved input failing the schema at runtime with no platform call made; output violating the
      output schema; an upgrade that removes an output field something binds to; a schema using a keyword
      the validator does not support; the `continue` warning; the `route` branch receiving the structured
      error.
13. **Rollout plan** — enablement per tenant behind a flag; the order in which agent classes are enabled;
    approval gates for action-taking agents if recommended; the signals watched during rollout; and the
    rollback triggers, such as a rising `UNKNOWN_OUTCOME` rate, detected duplicates, validation-failure
    spikes, or orphaned runs.
14. **Operations readiness** — the metrics, alerts, and runbooks that must exist before go-live: records
    stuck pending or running, `UNKNOWN_OUTCOME` rate, orphaned runs, detected duplicates, stream
    reconnects, and input and output validation failures, with a runbook for the ambiguous case.
15. **Asks of the platform team** — prioritised, each tied to the failure it prevents and the phase it
    unblocks.
16. **Risks and open questions** — each risk with likelihood, impact, mitigation, and owning role; each open
    question with what it blocks.

## Quality bar

- The output is a plan. If you notice yourself writing code, SQL, configuration, or an example payload,
  replace it with a description, a field table, or a decision table.
- Walk through each crash point explicitly. Do not claim exactly-once execution unless the spec supports it.
- Be concrete: real field names, status values, event names, and schema keywords from the spec.
- Keep assumptions few and visible, and label every one.
- Every task and phase traces to a requirement; every phase has verifiable exit criteria.
- Use tables for decisions, mermaid for flows, and prose for reasoning.
- The plan must not permit these traps:
  - sending input to the platform without validating it against the pinned input schema first;
  - letting downstream nodes bind to streamed partial output instead of the validated final result;
  - trusting the contract snapshot at publish instead of re-fetching it;
  - treating an unsupported schema keyword as if it validated;
  - comparing request bodies byte-for-byte instead of as canonical JSON;
  - calling the platform before the intent record is committed;
  - treating a dropped stream or a cancellation as an execution failure;
  - retrying every error, or inferring retryability from status codes when the spec provides a signal;
  - a `node_path` that omits loop iteration or sub-workflow nesting;
  - assuming a status or polling endpoint the spec does not show;
  - holding platform credentials in the browser, or trusting a tenant supplied by the client;
  - retaining raw sensitive inputs indefinitely, or writing them to general logs;
  - enabling action-taking agents before the ambiguous-case policy and reconciliation are in place;
  - adding components, endpoints, or tasks that nothing requires.
