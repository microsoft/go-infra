# Release UI prototype

This command contains the release coordination prototype and the local release-management UI.
The server runs on the release runner's machine and opens in their default browser.

Subcommands:

* `releaseagent serve` starts the local release UI.
* `releaseagent write-mermaid-diagram` writes a Mermaid diagram of the broader release process.

The landing page is a release dashboard. It lists work tracked by the current durable session and a
validated registry of release processes. Go images provides local planning, execution, and
monitoring. Go infrastructure and the complete Microsoft Build of Go release process remain future
additions rather than being mixed into the go-images workflow.

Each registry entry owns its dashboard metadata, documented release methods, inputs, dependency
graph, and optional server callbacks. The server derives `/{ID}` and every process API route from
that entry. A single `process.html` template and its generic JavaScript render every process.

## Adding a release process

Add one `ProcessDefinition` to `defaultProcessRegistry` in
`internal/releaseui/process_registry.go`. No HTML, JavaScript, or route change is required.

| Field | Purpose |
| --- | --- |
| `ID` | Stable machine-readable identifier used by registry lookups and APIs, such as `example-process`. |
| `Name` | User-facing process name shown on the dashboard and process page. |
| `Mark` | Short visual abbreviation shown on the dashboard card, such as `EX`. |
| `Description` | Brief dashboard explanation of what the process releases. |
| `Status` | User-facing badge text, such as `Available`, `Planned`, or `Future`. This is display-only; `Available` controls whether the process can be opened. |
| `Available` | Whether the dashboard card links to the process page. |
| `DocumentationURL` | Canonical HTTPS release instructions linked from the process page. |
| `Methods` | Documented external release paths. Use these when GitHub or another authenticated UI owns execution. |
| `Workflow` | Optional in-UI inputs, dependency steps, and server callbacks. |

For an external process, fill `Methods` and stop. For an in-UI process, describe the form and graph
with `ProcessInput` and `ProcessStep`. Set only the callbacks it needs:

```go
ProcessDefinition{
    ID: "example", Name: "Example", Mark: "EX", Description: "Release the example.",
    Status: "Available", Available: true,
    Workflow: &ProcessWorkflow{
        Heading: "Configure release", SubmitLabel: "Prepare release",
        Inputs: []ProcessInput{{ID: "version", Type: "text", Label: "Version", Required: true}},
        Steps: []ProcessStep{
          {Name: "Verify release"},
          {Name: "Publish release", DependsOn: []string{"Verify release"}},
        },
        GetPlan: (*Server).handleExampleGetPlan,
        Prepare: (*Server).handleExamplePlan,
        Start:   (*Server).handleExampleStart,
    },
}
```

`Preflight`, `GetPlan`/`Prepare`, `Simulate`, and `Start` are optional Go callbacks. `GetPlan` and
`Prepare` are a pair: one restores the current plan and one creates it. Their routes are generated
from the process ID; do not register routes or add browser code. Keep credentials, target
allowlists, input validation, checkpointing, duplicate prevention, and external calls inside those
Go boundaries. Go-images is the complete reference implementation.

With read-only Azure access enabled, **Track ongoing releases** discovers waiting and running pipeline `1023` builds and refreshes their status every 15 seconds.
These live Azure entries are merged with the durable local session and link directly to their Azure run; tracking never queues or changes a run.

## Go-images release modes

See the canonical [Golang toolset images release instructions](https://github.com/microsoft/go-lab/tree/main/docs/release#golang-toolset-images).

The go-images page targets `microsoft-go-images (official)` pipeline definition `1023`, repository `microsoft-go-images`, and branch `refs/heads/microsoft/main`.
It offers three explicit modes:

* **Normal** resolves the current `microsoft/main` tip, builds fresh images, and publishes to `public/`.
  The browser has no editable pipeline parameters.
* **Rollback / republish** accepts one positive pipeline `1023` build ID.
  The server verifies that the build succeeded, came from `microsoft/main`, and produced its own artifacts.
  It then uses that ID as `sourceBuildPipelineRunId` and publishes to `public/`.
  The build ID is the only editable pipeline input.
* **Test** resolves the current `microsoft/main` tip, builds fresh images, and fixes `publishRepoPrefix` to `dev/`.
  It still queues a real official build and may consume signing and agent resources, but it does not publish under `public/`.

The pipeline declares `publishRepoPrefix` as an unrestricted string whose official default is `public/`.
The release execution layer independently allowlists exactly `public/` for normal and rollback and `dev/` for test; arbitrary prefixes never cross the browser/API boundary.

All modes pin the exact current-main commit when the plan is created.
Immediately before the first queue attempt, the server resolves main again and rejects a stale plan if the branch advanced.
The first DAG step then polls the internal `microsoft-go-images` Azure Repos mirror for that exact SHA; the queue step cannot start until the commit is available.
The pipeline definition, branch, YAML path, complete parameter contract, and mode-derived parameter set are validated server-side.
Browser requests cannot provide a definition, branch, commit, mirror target, prefix, or arbitrary parameter map.

Release versions are read from `src/microsoft/versions.json` at an exact commit and displayed only as audit metadata.
There is no version input on the dashboard or go-images release page, and versions are not parameters of pipeline `1023`.

## Running locally

Authenticate Azure CLI, then start the fully enabled release UI:

```console
az login
go run ./cmd/releaseagent serve
```

By default, the UI stores its durable session under the operating system's user configuration directory.
Use `-session-file` only to override that location:

```console
go run ./cmd/releaseagent serve \
  -session-file /path/to/release-session.json
```

Every new real run uses a two-step **Run** then **Confirm run** interaction.
The second request must include explicit confirmation and the exact current plan digest, so a stale or changed plan is rejected.
Normal and rollback runs can publish production images under `public/`.
Test runs are fixed to `dev/`.
Do not confirm a real execution without authorization.

The review page renders the coordinator DAG as a left-to-right dependency graph.
Steps at the same dependency depth are grouped vertically and labeled as parallel work, so future release processes can expose fan-out and fan-in directly.
During the pipeline wait step, the existing SSE stream also shows the active Azure stage/job/task paths, stage/job/task completion counts, and a stage progress bar.
Lightweight build status is checked every five seconds; the much larger Azure timeline is refreshed every 30 seconds.
Timeline detail is ephemeral UI state and is not written to the durable session file.

Azure Pipeline definition metadata and timelines, plus Azure Repos reads, use the official Azure DevOps Go SDK.
The generated SDK's `build.Build` model omits run-level `templateParameters`.
Custom REST calls are therefore limited to build retrieval and listing, where those parameters are required, and parameterized queueing.
The generated `Build.Parameters` field can carry the legacy correlation variables, but `QueueBuild` cannot emit the `templateParameters` required for the full queue payload.

## Durability and duplicate prevention

The focused graph checkpoints queue intent **before** issuing the Azure POST, then checkpoints the returned build ID and successful completion.
Correlation variables bind an Azure run to the local session, release mode, execution digest, source build, source commit, and version metadata.
If the process restarts in the queue-response crash window, it reconciles recent runs before attempting another POST.

When startup restores an incomplete session that already has a build ID, monitoring resumes automatically and checkpoints the terminal result.
The restored path wraps the execution service in a queue-denying adapter, so it can read the existing run but cannot queue a new one.

The session document is schema-versioned, structurally fingerprinted, atomically replaced, and protected from concurrent cooperative processes by an adjacent lease file.
It contains no credentials.
Schema version 6 intentionally rejects the earlier import-first production-demo sessions.
Workflow revision 7 uses unique step names as graph identity and rejects revision-6 plans.
It automatically migrates the exact revision-5 go-images plan only when it already contains a checkpointed build ID; the restored path can then monitor that run without queue authority.
A revision-5 queue attempt with no build ID remains rejected because its queue status is uncertain.
Start a new session file for any other earlier workflow.

The current file store owns one durable release at a time.
Dashboard responses already model ongoing and recent releases as lists so a future multi-session store can expand tracking without changing the browser API.

If a process terminates without cleaning up its lease, verify no release UI process is using the session and remove the adjacent `.lock` file manually.

## Security boundaries

* The server binds only to a loopback address.
* A random one-time launch token establishes an HTTP-only, same-site session cookie.
* State-changing requests require a matching Origin header.
* Azure CLI tokens are acquired on demand, cached only in memory, and never sent to the browser, logged, or persisted.
* The generic Azure pipeline client is read-only.
* The dedicated queue client can only POST definition `1023` on `refs/heads/microsoft/main` with a server-derived normal, rollback, or test parameter set.
* The durable session stores non-secret input, state, structural and execution digests, and no credentials.

See [ADR 0020: Create UI for release management](https://github.com/microsoft/go-lab/blob/main/docs/adr/0020-microsoft-release-ui-for-go.md) for the accepted local-server design.
The earlier graph prototype came from [ADR 0005: Use a release agent to coordinate releases](https://github.com/microsoft/go-lab/blob/main/docs/adr/0005-use-a-release-agent-to-coordinate-releases.md).
