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

The first integration is intentionally focused on the direct `microsoft-go-images (official)`
pipeline (definition `1023`). It can:

* Validate Microsoft release versions used to find matching source commits.
* Display a three-step queue, monitor, and complete graph for pipeline `1023`.
* Preview all three runtime defaults: fixed informational `_info`,
	`sourceBuildPipelineRunId=$(Build.BuildId)`, and `publishRepoPrefix=public/`.
* Run that focused graph as an in-memory simulation and stream status changes to the browser.
* Optionally persist and restore the non-secret release plan using `-session-file <path>`.
* Report local readiness, including whether `gh` and `az` executables are present, without running
	them or checking authentication.

By default it cannot contact GitHub, Azure DevOps, or any publishing service. Queueing remains
hard-disabled in every mode.

For example, this preserves the plan across process restarts:

```console
go run ./cmd/releaseagent serve -session-file "$HOME/.config/microsoft-go/release-session.json"
```

The session document is schema-versioned, structurally fingerprinted, written using an atomic file
replacement, and protected from concurrent cooperative processes by a lease file. It contains no
credentials. If the process terminates without cleaning up the lease, verify no release UI process
is using the session and remove the adjacent `.lock` file manually.

The focused graph checkpoints the pipeline build ID and successful completion immediately
after they are recorded. A failed checkpoint stops subsequent work until the pending state can be
saved.

External execution remains hard-disabled. Default startup only discovers `gh` and `az` in `PATH`;
it does not invoke either command. Explicit read-only mode invokes `az` only to authenticate Azure
DevOps GET requests.

## Pre-production testing

The focused workflow has hermetic tests for:

* Exact pipeline `1023` parameter generation.
* Queue, monitor, checkpoint, and restart/resume behavior using mocks.
* Azure CLI token command construction using a fake command runner.
* Azure DevOps HTTP request serialization using a loopback `httptest` server.
* Status/result normalization, authentication errors, cancellation, and token redaction.
* Read-only candidate discovery, selected-build revalidation, import, and monitoring.

The Azure client and token provider are wired only behind an explicit, default-off read-only flag,
an exact definition/repository/YAML allowlist, live validation of all parameter names, types,
defaults, and allowed values, and durable state. The server receives search, validation, and
monitor callbacks—not a queue-capable service.

### Read-only pipeline 1023 discovery

Authenticated read-only discovery and monitoring can be enabled for production definition `1023`:

```console
go run ./cmd/releaseagent serve \
	-session-file "$HOME/.config/microsoft-go/release-session.json" \
	-enable-go-images-azure-read-only
```

Pipeline `1023` does not accept release versions directly. Discovery lists recent runs, reads
`src/microsoft/versions.json` at each run's exact `sourceVersion` from the internal
`microsoft-go-images` repository, and keeps runs whose source contains every requested version.
Selecting a candidate re-fetches and revalidates its definition, source commit, and versions before
creating a local session. A running imported build can be monitored by ID using read-only GETs.

There is deliberately no real queue endpoint. Pipeline `1023` builds, signs, and publishes images,
and its official default is `publishRepoPrefix=public/`; unlike the former wrapper-pipeline smoke
path, it has no switch that disables all release effects.

The server only binds to a loopback address. A random one-time launch token establishes an
HTTP-only, same-site session cookie, and state-changing requests require a matching Origin header.

See [ADR 0020: Create UI for release management](https://github.com/microsoft/go-lab/blob/main/docs/adr/0020-microsoft-release-ui-for-go.md)
for the accepted design. The earlier graph prototype came from
[ADR 0005: Use a release agent to coordinate releases](https://github.com/microsoft/go-lab/blob/main/docs/adr/0005-use-a-release-agent-to-coordinate-releases.md).
