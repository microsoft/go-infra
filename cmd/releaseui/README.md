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
Its `Identity` method provides shared dashboard metadata, and `Processes` returns one
`contract.Process` for each distinct form and release behavior.
Pass the group to `releaseui.WithProcesses`.
No HTML, JavaScript, or route change is required.

Keep process policy separate from reusable mechanics:

```text
releaseui/                           Local HTTP lifecycle, UI, execution, and built-in storage
releaseui/contract/                  Process and durable-state contracts
releaseui/snapshot/                  Typed JSON encoding and strict snapshot decoding
releaseui/releaseinput/              Process input definitions, parsing, and Go field bindings
releaseui/internal/azdorunstore/     Azure Boards release storage
releaseui/internal/runstore/         Private durable-state and storage contracts
azdo/token/                          Azure CLI token acquisition and caching
azdo/workitem/                       General Azure Boards work-item client
cmd/releaseui/internal/azdo/pipeline/ Azure Pipeline reads and queue transport
cmd/releaseui/internal/azdo/repo/     Azure Repos reads
cmd/releaseui/internal/githubclient/ Authenticated GitHub operations
cmd/releaseui/internal/goimages/     Go-images targets, allowlists, state, and graph
cmd/releaseui/internal/goinfra/      Go-infra targets, allowlists, state, and graph
```

The reusable host uses its private Azure Boards store by default. Callers that need another backend
can supply `releaseui.WithReleaseRunStore`. The neutral clients accept service-level requests. A release package decides which repository,
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

func (p *exampleProcess) InputForm(inputs *releaseinput.Set) any {
	result := new(exampleInput)
	inputs.PositiveIntVar(&result.RunID, releaseinput.FieldOptions{Label: "Run ID"})
	return result
}
```

Releaseui assigns field IDs in registration order, so `InputForm` must register the same fields in the same order each time.
These IDs connect browser fields to Go bindings; they do not change the process's persisted input schema.
Preparing a plan leaves the browser's input controls and their values intact, whether preparation succeeds or fails.
`Process.Prepare` validates the typed form result and returns a `StateSnapshot`.
`Process.Validate` checks snapshot input and state before the host accepts prepared state, loads a run, imports a checkpoint, or saves captured state.
It must accept valid intermediate states without changing the snapshot or performing external I/O.
During preparation, errors marked with `releaseinput.ErrInvalidInput` appear beside the input form and remain visible until the inputs change or preparation is retried.
To associate a validation error with a field, return `releaseinput.NewInvalidInputError(&result.RunID, "run ID must identify a successful build")` from `Prepare`.
Releaseui matches the pointer against the fields registered in `releaseinput.Set`, displays the message beside the matching input, and gives that input a red outline.
A nil or unmatched pointer displays the message at the form level.
The error still matches `releaseinput.ErrInvalidInput` through `errors.Is`, and field matching works through wrapped errors.
`Process.Load` decodes the validated snapshot and constructs a `Run`; it does not need to repeat validation.
`Run.TakeSnapshot` returns an independent durable state copy and an error if capture or encoding fails.
`Run.TakeView` reports current display state, `Run.Plan` provides confirmation content, and `Run.Build` constructs the executable graph.
The shared host names new tracking items from the registered process-group and process names.
Process implementations do not supply tracking titles. Pipeline identifiers and step counts
remain in plan facts and review-page subtitles. Updates do not rename existing tracking items.
Only execution of the returned steps may mutate external state.
Each step function receives a context for cancellation and deadlines and an explicit
`coordinator.StepControl` for progress reporting. Call `ctl.ReportProgress` to publish step
progress, and pass the control explicitly to polling helpers that report progress. The runner
provides a control bound to each executing step; progress reporting does not use context values.
Change run state through `ctl.Update`, which captures the resulting snapshot and view under the run's shared state lock.
The host releases that lock before storage I/O and saves updates in mutation order.
`Update` captures state and attempts a checkpoint before returning, without returning an error.
It always calls its callback under the state lock, even after cancellation.
Storage failures display a warning but do not stop the release.
Later updates and five-second heartbeats retry saving the latest captured state.
Call `ctl.EnsurePersisted()` and return its error before an external action that requires durable intent.
It retries pending persistence and succeeds only when all preceding updates have been saved.
Capture and validation failures still cancel execution and preserve the original error.
After such a failure, later `Update` calls still run their callbacks but skip capture and persistence.
Finalization attempts to save the latest valid state and reports any unresolved save failure.
`Run.Build` takes no arguments.
Execution contexts are supplied to step functions when they run.
Process implementations do not need their own locks for state changed through `Update`.
Use step dependencies to order reads of completed results.
Use `ctl.Read` for short, read-only access to run state inside steps.
`Read` always calls its callback, including after cancellation, so local results are initialized.
It holds the run's shared read lock in normal builds, but skips locking in race builds so missing step dependencies remain detectable.
Host snapshot and view reads always lock, including in race builds.
Do not nest `Read` or `Update` callbacks or wait for another step inside them.
Release-specific packages own target policy and call neutral Azure or GitHub clients for transport.
The server owns confirmation, its persistence envelope, duplicate-start protection, checkpoints, restart behavior, state APIs, and event streaming.

`Process.Preflight` returns separate warning and blocking errors.
A warning is displayed but permits a new release.
A blocking error prevents preparation and starting, but does not prevent restoring or continuing an existing run.

Before preparation, the shared lifecycle creates a fresh input value with `InputForm`.
`releaseinput.Set.Parse` rejects missing or unknown fields and binds valid positive integers directly to typed Go fields.
Each process then applies its semantic and fixed-target validation.

## Demo processes

To include two independent simulated releases, start the UI with the demo flag:

```console
go run ./cmd/releaseui serve -demo
```

Confirmation creates real Azure DevOps test work items, so authenticate `az` before running a demo.
No builds, workflows, or artifacts are published.
Each dimension simulates 30 seconds of analysis, then saves the determinant of the scaled identity matrix `(1 + 1/n) I_n`.
Its determinant, `(1 + 1/n)^n`, approaches Euler's number `e` as the dimension `n` increases.
The final step fits a straight line to `n × determinant(n)` against `n` using every dimension and saves its slope as "the number."
Since `n × determinant(n)` approaches `e × n - e/2`, the slope estimates `e`.
With only one dimension, the estimate is that dimension's determinant.
Elapsed-time progress is transient: resuming skips completed dimensions and restarts unfinished ones from zero.
Rebellious scientists sabotage dimension 1 halfway through each attempt without saving a partial result.
The saved-estimate snapshot uses new process IDs; older determinant-only, prime-result, and work-count snapshots are not compatible.
See the [demo implementation and API simplification plans](internal/demo/README.md) for the example's structure and proposed API changes.

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
Manual patch releases also require the correlation input and matching run name in the workflow on
`main`. If that workflow update has not landed, the UI explains the prerequisite and blocks manual
dispatch without blocking release-on-merge.
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
Tracking items start in **Active**. Saving a release outcome does not close or reopen the item.
Close tracking items manually in Azure DevOps, completing any project-required resolution fields.
Successful runs still appear under **Recent history** while their tracking items remain open.
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

The dashboard queries active tagged work items and the ten most recently closed items.
If an individual work item fails validation or its stored record cannot be decoded, it appears as a
read-only card. Open work items appear under **Needs attention**; closed items appear under
**Recent history**. The card links to its tracking record, shows its saved release status when
readable separately from its work-item state, and gives safe next steps.
Saved release titles and update times identify these entries even when the full run cannot be
loaded. Tracking item numbers remain in the secondary link rather than serving as release names.
The underlying error is available under **Technical details**. These cards cannot open, resume, or
edit the unreadable release state; other readable releases remain available. No migration or
record rewrite happens automatically. Closing a work item does not cancel its external run or
update the stored execution result.
The current prototype can show older test records as **Cannot open in this session**.
This is an unreleased prototype. Compatibility with data from earlier prototype builds is not
guaranteed, and older test records may remain read-only. Supported current-format records can
still be reopened in another UI session.
The runner publishes a self-contained graph view alongside its execution checkpoint. It contains
step names, dependencies, progress, errors, and external-run links. The shared writer saves each
checkpoint, publishes progress and a heartbeat every five seconds, and saves the final outcome.
All writes use the same revision-checked persistence path.

Opening a release reads only this published view. It does not load the process checkpoint,
construct executable steps, or query Azure builds or GitHub workflows. The view can be opened
even when the matching process is not configured or its checkpoint uses an unsupported format.
The dashboard uses the same published data.

Release pages refresh the stored view every five seconds and show its last-published timestamp.
The host assigns this timestamp when publishing checkpoints, progress, heartbeats, and the final outcome.
Processes report live step progress through `ctl.ReportProgress`; `RunView` does not carry a timestamp.
The dashboard and saved release page show the last heartbeat timestamp for unfinished runs.
The dashboard refreshes its saved records every 15 seconds, so an active worker's newer heartbeats
replace the displayed timestamp. Saved release pages refresh every five seconds.
Dashboard refreshes preserve keyboard focus on a card or action that remains available.
After a minute without a heartbeat, an executing run is labeled **Probably interrupted**.
This label does not change the stored record or automatically resume the run.
The page keeps the last reported step progress. It does not independently monitor or restart the
external operation. Use a step's **Open external run** link or **Open release run** to inspect the
provider directly.

Records whose last publication reports an executing runner appear under **Ongoing releases**.
Open failed, canceled, uncertain, and interrupted unfinished work appears
under **Needs attention**. Successful work and manually closed terminal outcomes appear under
**Recent history** without rewriting the execution result. Closed unfinished records remain read-only.
Clicking a card or **Open** opens its published release page. Its Azure DevOps browser link appears
on the dashboard and release page. **View JSON** explicitly exports the execution checkpoint for
manual repair and imports edited JSON only when the exported revision is still current. Import
validates the process payload and executable graph before updating the record. It cannot change
the process ID, input, or immutable intent digest. Stop the previous runner before
repairing that release's state. Viewing any number of releases is independent of the configured
execution sessions. Executing a different release for the same process still requires restarting
that process session.

Every new real run uses a two-step **Run** then **Confirm run** interaction.
The second request must include explicit confirmation and the exact current plan digest, so a stale or changed plan is rejected.
An open unfinished record offers **Resume release** then **Confirm resume** when its process is
configured and it is not executing, or its last heartbeat is at least a minute old.
Only that confirmed operation loads the checkpoint.
Resume builds the saved run's graph in a fresh runner.
It rechecks execution readiness and claims the reviewed record revision before any actions run.
The prototype assumes a single operator. Confirmed Resume can replace an old `executing`
reservation without a separate JSON repair. The backend rejects a recently updated record,
a changed revision, a closed or completed run, or an execution already active in this server.
This is not a multi-user handoff mechanism. Opening a release remains view-only.
On Ctrl+C, the server stops accepting new executions, cancels local work, and waits for final
checkpoint saves before exiting. External pipelines and workflows are not canceled.
Shutdown has a 15-second deadline and reports errors if local work or final saves cannot finish.
An unfinished run with a saved recovery checkpoint appears as **Interrupted** after a successful
shutdown save. **Resume release** continues that checkpoint. The Go-images queue step skips
creating a build when its ID is already saved, and the wait step checks that existing build.
Normal and rollback runs can publish production images under `public/`.
Test runs are fixed to `dev/`.
Do not confirm a real execution without authorization.

The review page renders the coordinator DAG as a left-to-right dependency graph.
The current go-images graph is a linear mirror, queue, and monitor sequence.
During the pipeline wait step, the existing SSE stream reports whether the Azure build is queued,
running, or complete. Build status is checked every five seconds, and the UI links to Azure DevOps
for stage, job, and task details.
Once an external run is known, **Open release run** links to the Azure pipeline or GitHub workflow.
The tracking-record link remains separate. Subsequent checkpoints also include the external-run
link in the work-item description.

Azure Pipeline definition metadata and Azure Repos reads use the official Azure DevOps Go SDK.
The generated SDK's `build.Build` model omits run-level `templateParameters`.
Custom REST calls are therefore limited to build retrieval and listing, where those parameters are required, and parameterized queueing.
The generated `Build.Parameters` field can carry the legacy correlation variables, but `QueueBuild` cannot emit the `templateParameters` required for the full queue payload.

## Durability and duplicate prevention

The focused graph checkpoints queue intent **before** issuing the Azure POST, then checkpoints the returned build ID and successful completion.
Confirmed external cancellation is saved as canceled. Stopping the local monitor is different:
it leaves a checkpoint that can be resumed rather than pretending the external run was canceled.
The UI reports completion only after the final record save, and shows persistence failures instead
of reporting success.
Correlation variables bind an Azure run to the release session ID, release mode, execution digest, source build, source commit, and version metadata.
If the process restarts in the queue-response crash window, it reconciles recent runs before attempting another POST.

Neither startup nor opening a saved record resumes execution automatically. Only a confirmed
resume runs its steps. Process-owned checkpoints and reconciliation still determine whether a
step can continue without duplicating an external action.

The Go-images document stores only standalone input and state. `Run.Build` reconstructs the
executable graph without running it. The shared lifecycle writes a separate display snapshot
alongside the process checkpoint. Execution recovery never uses the display snapshot.
The release UI stores the document inside the
process-owned payload and checks the work-item revision on every update. It contains no credentials.
Go-images and Go-infra payloads and checkpoints each carry process-owned schema version 1.

The server keeps one selected release per configured process. Durable release state lives
in its Azure DevOps work item. The server creates the work item before calling the target
service, updates it with an Azure DevOps revision check, and resumes monitoring a known external run
after explicit restore. If a run cannot be correlated, the restored action becomes `uncertain` and
refuses a replacement.

State and progress views remain responsive while state is saved or a release is restored.
If a new plan is prepared during a restore, the restore returns a conflict rather than replacing
that plan.

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
