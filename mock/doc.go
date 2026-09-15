// Package mock is an in-memory agent platform that implements api/openapi.yaml, for integration
// tests in this repo and in the repos that import it.
//
// Start one per test, seed the catalog, and script what executions do:
//
//	srv := mock.NewTestServer(t, mock.Options{})
//	if err := srv.AddAgentVersion(mock.AlertTriage()); err != nil {
//		t.Fatal(err)
//	}
//	srv.Script(mock.Match{AgentID: "alert-triage"},
//		mock.Fail(platform.CodeProviderUnavailable),                    // attempt 1: retryable failure
//		mock.Succeed(map[string]any{"severity": "low", "verdict": "benign"}).After(200*time.Millisecond), // attempt 2: 202, then poll
//	)
//	// Point the code under test at srv.URL, then assert on srv.Executions() and srv.Calls().
//
// Outcomes cover the scenarios of Step 1 in docs/design/agent-nodes.md:
//
//   - success: the default, or Succeed. createExecution never waits: with zero latency the
//     execution is already terminal and returns 200.
//   - slow success: Succeed(...).After(d), or Options.Latency, returns 202 with Location, and
//     getExecution returns the result once d elapses.
//   - retryable and non-retryable failures: Fail for execution-level failures (200 with status
//     failed), Reject for request-level rejections (non-2xx, no execution created).
//   - timeouts: Hang (or a delay past timeout_ms) fails the execution with TIMEOUT when timeout_ms
//     elapses, scaled by Options.TimeoutScale. Hang().IgnoreTimeout() never finishes on its own.
//   - cancellation: cancelExecution moves a running execution to cancelled; ExecutionRecord.CancelCalls
//     counts the calls.
//
// Idempotency follows the spec: the same Idempotency-Key and body replays the recorded execution
// without starting a new one, and a different body gets 409 IDEMPOTENCY_KEY_REUSED.
package mock
