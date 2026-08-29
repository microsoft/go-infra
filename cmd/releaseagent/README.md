# Release UI prototype

This command contains the release coordination prototype and the local release-management UI.
The server runs on the release runner's machine and opens in their default browser.

`releaseagent serve` starts the local release UI.

The landing page lists work tracked by the current durable session and the two implemented release
processes. Go images provides local planning, execution, and monitoring. Go infrastructure provides
reviewed, confirmed actions for the two GitHub-owned patch release paths documented by the team.

Each registry entry owns its dashboard metadata, inputs, and workflow callbacks. The server derives
the process page and API routes from that entry. One HTML page and JavaScript implementation render
both processes. Browser-editable inputs are limited to fixed choices and positive integer IDs.

## Adding a release process

Add one `ProcessDefinition` to `defaultProcessRegistry` in
`internal/releaseui/process_registry.go`. No HTML, JavaScript, or route change is required.

| Field | Purpose |
| --- | --- |
| `ID` | Stable machine-readable identifier used by registry lookups and APIs, such as `go-infra`. |
| `Name` | User-facing process name shown on the dashboard and process page. |
| `Mark` | Short visual abbreviation shown on the dashboard card, such as `IN`. |
| `Description` | Brief dashboard explanation of what the process releases. |
| `DocumentationURL` | Canonical HTTPS release instructions linked from the process page. |
| `Workflow` | Required in-UI inputs and execution behavior. |

For a reviewed, durable external action, describe the form with `ProcessInput`, then set `DurableAction`:

```go
ProcessDefinition{
    ID: "example", Name: "Example", Mark: "EX", Description: "Release the example.",
    Workflow: &ProcessWorkflow{
        Heading: "Configure release", SubmitLabel: "Prepare release",
    Inputs: []ProcessInput{{ID: "run", Type: "number", Label: "Run ID", Required: true}},
        DurableAction: true,
    },
}
```

  Supply one `ProcessExecutor` under the same process ID and one shared `ProcessRunStore`. The store holds the server's single current durable external action. The executor owns process policy through `Preflight`, `Prepare`, `Execute`, `Resume`, and `Validate`. The server owns confirmation, duplicate-start protection, checkpoints, restart behavior, state APIs, and event streaming.

  Before preparation, the shared lifecycle validates request keys, required and conditional fields, choice values, positive integer syntax, and defaults. Executors then apply process-specific semantic and fixed-target validation.

  `Preflight`, `GetPlan`/`Prepare`, `Simulate`, and `Start` remain available for custom lifecycles. Do not combine these handlers with `DurableAction`.

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

## Go-infra patch releases

The go-infra page follows the canonical
[microsoft/go-infra release instructions](https://github.com/microsoft/go-lab/tree/main/docs/release#microsoftgo-infra)
and exposes both supported paths:

* **Release on merge** accepts one pull request number. The server verifies that the PR is open, targets `main`, and is not from a fork, then prepares a request to add the `release-on-merge` label. Starting the action rechecks the exact PR head SHA. The UI never merges the PR; the existing workflow creates the release only after the labeled PR is merged.
* **Manual patch release** dispatches only `create-go-infra-patch-release.yml` on `main`. Dry-run
  mode sets `dry-run` to `true` and only calculates the next version. Publish mode sets it to
  `false` and can create the next patch release. After GitHub accepts the dispatch, the server
  discovers the new run, journals its ID and URL, and polls it to a terminal conclusion. GitHub's workflow dispatch endpoint does not return a run ID, so the UI supplies a random token as the run title and matches that exact title. If a monitoring interval times out, polling continues from the checkpointed run ID. The dashboard reports the final result.

Both paths require an authenticated `gh` CLI, a reviewed plan, and a separate confirmation click.
The server hardcodes `microsoft/go-infra`, `main`,
`release-on-merge`, and the workflow filename; browser input cannot replace any of those targets.

## Running locally

Start the fully configured release UI without arguments:

```console
go run ./cmd/releaseagent serve
```

Authenticate `az` before using Azure-backed go-images actions and `gh` before using GitHub-backed
go-infra actions. Missing authentication is reported by process preflight; it does not require a
different server startup mode.

By default, the UI stores its durable session under the operating system's user configuration directory.
Use `-session-file` only to override that location:

```console
go run ./cmd/releaseagent serve \
  -session-file /path/to/release-session.json
```

The configured session path also derives an adjacent durable process-run journal. Starting the
server does not perform an external action. Opening the go-infra page performs read-only preflight
checks; a mutation still requires preparing the exact request and confirming it.

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

The go-images session document is schema-versioned, structurally fingerprinted, atomically replaced, and protected from concurrent cooperative processes by an adjacent lease file.
It contains no credentials.
Schema version 7 stores only standalone go-images input and state. It intentionally rejects the
older full-release-shaped prototype documents rather than retaining a second domain model and
migration path. Workflow revision 7 uses unique step names as graph identity. Start with a new
session file when either version is unsupported.

The current session and process-run stores together own one active durable release at a time.
Dashboard responses include both go-images sessions and durable process runs as process-neutral
ongoing and recent release lists, so a future multi-session store can expand tracking without
changing the browser API.

Durable external actions use an adjacent, schema-versioned atomic process-run journal derived from `-session-file`. The shared executor checkpoints `started` before calling the target service and resumes monitoring a known external run after restart. If a run cannot be correlated, the next startup marks the action `uncertain` and refuses a replacement. The local file protects one machine; it is not a shared handoff mechanism for an unexpected outage. A future `ProcessRunStore` implementation can use a work item or another shared store without changing process policy.

If a process terminates without cleaning up its lease, verify no release UI process is using the
session and remove the adjacent `.lock` file manually.

## Security boundaries

* The server binds only to a loopback address.
* A random one-time launch token establishes an HTTP-only, same-site session cookie.
* State-changing requests require a matching Origin header.
* Azure CLI tokens are acquired on demand, cached only in memory, and never sent to the browser,
  logged, or persisted.
* Go-infra uses the locally authenticated `gh` CLI. JSON mutation bodies are sent through stdin, and the GitHub host, repository, ref, label, and workflow are hardcoded server-side.
* The generic Azure pipeline client is read-only.
* The dedicated queue client can only POST definition `1023` on `refs/heads/microsoft/main` with a server-derived normal, rollback, or test parameter set.
* The durable session stores non-secret input, state, structural and execution digests, and no credentials.

See [ADR 0020: Create UI for release management](https://github.com/microsoft/go-lab/blob/main/docs/adr/0020-microsoft-release-ui-for-go.md) for the accepted local-server design.
The earlier graph prototype came from [ADR 0005: Use a release agent to coordinate releases](https://github.com/microsoft/go-lab/blob/main/docs/adr/0005-use-a-release-agent-to-coordinate-releases.md).
