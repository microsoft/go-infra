# Release UI prototype

This command contains the release coordination prototype and the first local release UI iteration.
The UI runs an HTTP server on the release runner's machine and opens it in their default browser.

Subcommands:

* `releaseagent serve` - Start the local release UI.
* `releaseagent write-mermaid-diagram` - Writes a mermaid diagram showing the steps and dependencies of the release process.

Run the UI from the repository root:

```console
go run ./cmd/releaseagent serve
```

The first integration is intentionally focused on the existing
`microsoft-go-infra-release-go-images` pipeline (definition `1151`). It can:

* Validate versions, runner, optional tracking issue, and release variable group.
* Display a three-step queue, monitor, and complete graph for pipeline `1151`.
* Preview the exact pipeline parameters. Image build/publish and MAR verification are enabled;
	announcement publishing and DL updates are explicitly disabled.
* Run that focused graph as an in-memory simulation and stream status changes to the browser.
* Optionally persist and restore the non-secret release plan using `-session-file <path>`.
* Report local readiness, including whether `gh` and `az` executables are present, without running
	them or checking authentication.

It cannot contact GitHub, Azure DevOps, or any publishing service. Authentication, confirmation,
the real pipeline queue/monitor client, and reconciliation remain later iterations.

For example, this preserves the plan across process restarts:

```console
go run ./cmd/releaseagent serve -session-file "$HOME/.config/microsoft-go/release-session.json"
```

The session document is schema-versioned, structurally fingerprinted, written using an atomic file
replacement, and protected from concurrent cooperative processes by a lease file. It contains no
credentials. If the process terminates without cleaning up the lease, verify no release UI process
is using the session and remove the adjacent `.lock` file manually.

The focused graph checkpoints the release-pipeline build ID and successful completion immediately
after they are recorded. A failed checkpoint stops subsequent work until the pending state can be
saved. This does not by itself provide exactly-once queueing: the future production client must
still reconcile a build that may have been queued immediately before a process crash.

External execution remains hard-disabled. Discovering `gh` or `az` in `PATH` does not invoke those
commands and does not enable GitHub, Azure DevOps, or publishing operations.

## Pre-production testing

The focused workflow has hermetic tests for:

* Exact pipeline `1151` parameter generation.
* Queue, monitor, checkpoint, and restart/resume behavior using mocks.
* Azure CLI token command construction using a fake command runner.
* Azure DevOps HTTP request serialization using a loopback `httptest` server.
* Status/result normalization, authentication errors, cancellation, and token redaction.
* Correlation-based reconciliation that reuses an existing build rather than queueing twice.
* An end-to-end focused DAG run against a loopback fake Azure DevOps server.

The real Azure client and token provider are wired only behind an explicit, default-off smoke-test
flag, a fixed definition allowlist, a variable-group allowlist, durable state, and digest-bound
confirmation. Normal UI startup still cannot perform external operations.

### One-time pipeline 1151 smoke mode

An explicitly enabled mode is available for an authorized queue/monitor smoke test against
production definition `1151`. It requires a durable session and a server-allowlisted variable
group. The server forces `approveAheadOfTime=true`, `releaseIssue=nil`, and every release action
switch to `false`, so the orchestration run does not build/publish images, publish announcements,
update DL, query MAR, or report to a release issue.

```console
go run ./cmd/releaseagent serve \
	-session-file "$HOME/.config/microsoft-go/release-session.json" \
	-enable-go-images-smoke-test \
	-go-images-smoke-variable-group '<approved-variable-group>'
```

Starting in this mode performs authenticated read-only preflight but does not queue a run. Queueing
requires a persisted smoke plan, an exact plan-digest match, and typing
`QUEUE PIPELINE 1151 SMOKE TEST` in the browser. The session correlation ID is attached to the
Azure run so restart/retry reconciliation reuses an existing build instead of queueing another.

The server only binds to a loopback address. A random one-time launch token establishes an
HTTP-only, same-site session cookie, and state-changing requests require a matching Origin header.

See [ADR 0020: Create UI for release management](https://github.com/microsoft/go-lab/blob/main/docs/adr/0020-microsoft-release-ui-for-go.md)
for the accepted design. The earlier graph prototype came from
[ADR 0005: Use a release agent to coordinate releases](https://github.com/microsoft/go-lab/blob/main/docs/adr/0005-use-a-release-agent-to-coordinate-releases.md).
