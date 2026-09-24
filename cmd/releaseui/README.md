# Release UI prototype

This command contains the release coordination prototype and the local release-management UI.
The server runs on the release runner's machine and opens in their default browser.

`releaseui serve` starts the local release UI.

The landing page lists the two implemented release process groups.
Go images provides planning, execution, and monitoring.
Go infrastructure provides reviewed, confirmed actions for the GitHub-owned patch release paths documented by the team.

Each registered `contract.ProcessGroup` provides related concrete processes and shared dashboard metadata.
Each `contract.Process` owns one input form and one stable persisted-state identity.
It prepares a process-owned snapshot and loads a `contract.Run`, which owns mutable execution state and constructs its executable graph.
The shared server derives process pages and API routes from those contracts.

## Adding a release process

Add a package under `cmd/releaseui/internal` that implements `contract.ProcessGroup`.
Implement one `contract.Process` for each distinct form and release behavior, then pass the group to `releaseui.WithProcesses`.
No HTML, JavaScript, or route change is required.

Keep process policy separate from reusable mechanics:

```text
releaseui/                           Local HTTP lifecycle, UI, execution, and storage contracts
releaseui/contract/                  Process and durable-state contracts
azdo/workitem/                       Azure Boards release-store adapter
cmd/releaseui/internal/azdopipeline/ Azure Pipeline reads and queue transport
cmd/releaseui/internal/azdorepo/     Azure Repos reads
cmd/releaseui/internal/githubclient/ Authenticated GitHub operations
cmd/releaseui/internal/goimages/     Go-images targets, allowlists, state, and graph
cmd/releaseui/internal/goinfra/      Go-infra targets, allowlists, state, and graph
```

The reusable host sees only revisioned release records through `releaseui.ReleaseRunStore`; the
command supplies the Azure Boards implementation. The neutral clients accept service-level requests. A release package decides which repository,
pipeline, branch, workflow, labels, and parameters are allowed before calling them. The command
only parses flags, constructs those dependencies, and registers processes. `releaseui.ListenAndServe`
owns loopback binding, HTTP timeouts, serving, and graceful shutdown.

| Field | Purpose |
| --- | --- |
| `ID` | Stable machine-readable identifier used by registry lookups, persisted state, and APIs, such as `go-infra-publish`. |
| `Name` | User-facing process name shown on the dashboard and process page. |
| `Mark` | Short visual abbreviation shown on the dashboard card, such as `IN`. |
| `Description` | Brief dashboard explanation of what the process releases. |
| `DocumentationURL` | Canonical HTTPS release instructions linked from the process page. |
| `InputPreamble` | Text shown above the process input form. |
| `InputSubmitLabel` | Label for the button that prepares the process. |

The process returns its metadata from `Definition` and declares its form in `InputForm`:

```go
type exampleInput struct {
  RunID int
}

func (p *exampleProcess) Definition() contract.ProcessDefinition {
  return contract.ProcessDefinition{
    Identity: contract.Identity{Name: "Example", Description: "Release the example."},
    ID: "example",
    InputPreamble: "Configure the release.",
    InputSubmitLabel: "Prepare release",
  }
}

func (p *exampleProcess) InputForm(inputs *releaseflag.InputSet) any {
  result := new(exampleInput)
  inputs.PositiveIntVar(&result.RunID, "runId", releaseflag.FieldOptions{Label: "Run ID"})
  return result
}
```

`Process.Prepare` validates the typed form result and returns a `StateSnapshot`.
`Process.Load` validates a snapshot and constructs a `Run`.
`Run.TakeSnapshot` returns an independent durable state copy, `Run.TakeView` reports current display state, `Run.Plan` provides confirmation content, and `Run.Build` constructs the executable graph.
Only execution of the returned steps may mutate external state.
Release-specific packages own target policy and call neutral Azure or GitHub clients for transport.
The server owns confirmation, its persistence envelope, duplicate-start protection, checkpoints, restart behavior, state APIs, and event streaming.

`Process.Preflight` returns separate warning and blocking errors.
A warning is displayed but permits a new release.
A blocking error prevents preparation and starting, but does not prevent restoring or continuing an existing run.

Before preparation, the shared lifecycle creates a fresh input value with `InputForm`.
`InputSet.Parse` rejects missing or unknown fields and binds valid positive integers directly to typed Go fields.
Each process then applies its semantic and fixed-target validation.

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
  It still queues a real official build, creates a work item tagged `releaseagent`, `test`, and
  `go-images` when confirmed, and
  may consume signing and agent resources, but it does not publish under `public/`.

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
  mode sets `dry-run` to `true`, calculates the next version, and creates a work item tagged
  `releaseagent`, `test`, and `go-infra` when confirmed. Publish mode sets it to `false` and can
  create the next patch release. After
  GitHub accepts the dispatch, the server
  discovers the new run, checkpoints its ID and URL, and polls it to a terminal conclusion. GitHub's workflow dispatch endpoint does not return a run ID, so the UI supplies a random token as the run title and matches that exact title. If a monitoring interval times out, polling continues from the checkpointed run ID. The dashboard reports the final result.

Both paths require an authenticated `gh` CLI, a reviewed plan, and a separate confirmation click.
The server hardcodes `microsoft/go-infra`, `main`,
`release-on-merge`, and the workflow filename; browser input cannot replace any of those targets.

## Running locally

Start the release UI without additional storage configuration:

```console
go run ./cmd/releaseui serve
```

Authenticate `az` before starting the UI and `gh` before using GitHub-backed go-infra actions. Each
confirmed execution creates a tagged DEVDIV `Issue` under `DevDiv\GoLang`; this includes go-images
test mode and go-infra dry-run mode. Every item receives its process ID as a tag. Test and dry-run
items also receive `test`; all release UI queries combine these tags with `releaseagent`. Azure
DevOps treats tags as case-insensitive and may display the project-canonical casing, such as `Test`.
Unconfirmed plans remain in memory and create no work item.
To restore one explicitly, add `-release-work-item <id>`.
Starting the server does not perform an external action.
Opening the go-infra page performs read-only preflight checks; a mutation still requires preparing
the exact request and confirming it.

The release UI owns `System.Description` on these work items. It shows status and release type plus
useful process details. Go-images work items include mode, versions, publication prefix, source
commit, Azure build, and checkpoint timestamps. Go-infra work items include their intent, action,
reviewed facts, target, and external run. Links open the underlying commit, build, pull request, or
workflow run. The base64url-encoded canonical JSON remains in a collapsed managed-state section so
Azure DevOps HTML normalization cannot alter release state. Add operator notes as work-item comments
rather than editing Description.

The dashboard queries active tagged work items and the ten most recently closed items. Starting and
running work appears under **Ongoing releases**. Active failed, canceled, and uncertain work appears
under **Needs attention**. Successful work and manually closed terminal outcomes appear under
**Recently completed** without rewriting the execution result. Clicking a card or **Open** selects
one work item for this server. Its Azure DevOps browser link appears on the dashboard and selected
release page. **View JSON** exports its snapshot for manual repair and imports edited JSON only when
the exported Azure DevOps revision is still current. Import validates the process payload and cannot
change its process ID or immutable intent digest. Restart without a selected release before
repairing that release's state. Reopening the currently selected work item is idempotent; selecting a
different work item still requires restarting the server.

Every new real run uses a two-step **Run** then **Confirm run** interaction.
The second request must include explicit confirmation and the exact current plan digest, so a stale or changed plan is rejected.
Normal and rollback runs can publish production images under `public/`.
Test runs are fixed to `dev/`.
Do not confirm a real execution without authorization.

The review page renders the coordinator DAG as a left-to-right dependency graph.
The current go-images graph is a linear mirror, queue, and monitor sequence.
During the pipeline wait step, the existing SSE stream reports whether the Azure build is queued,
running, or complete. Build status is checked every five seconds, and the UI links to Azure DevOps
for stage, job, and task details.

Azure Pipeline definition metadata and Azure Repos reads use the official Azure DevOps Go SDK.
The generated SDK's `build.Build` model omits run-level `templateParameters`.
Custom REST calls are therefore limited to build retrieval and listing, where those parameters are required, and parameterized queueing.
The generated `Build.Parameters` field can carry the legacy correlation variables, but `QueueBuild` cannot emit the `templateParameters` required for the full queue payload.

## Durability and duplicate prevention

The focused graph checkpoints queue intent **before** issuing the Azure POST, then checkpoints the returned build ID and successful completion.
Correlation variables bind an Azure run to the release session ID, release mode, execution digest, source build, source commit, and version metadata.
If the process restarts in the queue-response crash window, it reconciles recent runs before attempting another POST.

When startup restores an incomplete session that already has a build ID, monitoring resumes automatically and checkpoints the terminal result.
The restored path wraps the execution service in a queue-denying adapter, so it can read the existing run but cannot queue a new one.

The Go-images document stores only standalone input and state. `Run.Build` reconstructs the
coordinator graph instead of serializing it. The release UI stores the document inside the
process-owned payload and checks the work-item revision on every update. It contains no credentials.
Go-images and Go-infra payloads and checkpoints each carry process-owned schema version 1.

The current server runs one selected release at a time. Go-images and Go-infra state live only
in the selected Azure DevOps work item. The server creates the work item before calling the target
service, updates it with an Azure DevOps revision check, and resumes monitoring a known external run
after explicit restore. If a run cannot be correlated, the restored action becomes `uncertain` and
refuses a replacement.

## Security boundaries

* The server binds only to a loopback address.
* A random one-time launch token establishes an HTTP-only, same-site session cookie.
* State-changing requests require a matching Origin header.
* Azure CLI tokens are acquired on demand, cached only in memory, and never sent to the browser,
  logged, or persisted.
* Go-infra uses the locally authenticated `gh` CLI. JSON mutation bodies are sent through stdin, and the GitHub host, repository, ref, label, and workflow are hardcoded server-side.
* The generic Azure pipeline client is read-only.
* The dedicated queue client can only POST definition `1023` on `refs/heads/microsoft/main` with a server-derived normal, rollback, or test parameter set.
* Release work items store non-secret input, state, and immutable intent digests, and no credentials.

See [ADR 0020: Create UI for release management](https://github.com/microsoft/go-lab/blob/main/docs/adr/0020-microsoft-release-ui-for-go.md) for the accepted local-server design.
