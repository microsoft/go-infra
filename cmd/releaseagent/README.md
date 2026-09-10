# Release UI prototype

This command contains the release coordination prototype and the local release-management UI.
The server runs on the release runner's machine and opens in their default browser.

`releaseagent serve` starts the local release UI.

The landing page lists the two implemented release processes. Go images provides planning,
execution, and monitoring. Go infrastructure provides reviewed, confirmed actions for the two
GitHub-owned patch release paths documented by the team.

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
    Workflow: ProcessWorkflow{
        Heading: "Configure release", SubmitLabel: "Prepare release",
        Inputs: []ProcessInput{{ID: "run", Type: "number", Label: "Run ID"}},
        DurableAction: true,
    },
}
```

Supply one `ProcessExecutor` under the same process ID and one shared `ProcessRunStore`. The store
creates an Azure DevOps work item only after confirmation and updates it by revision at durable
checkpoints. The executor owns process policy through `Preflight`, `Prepare`, `Execute`, `Resume`,
and `Validate`. The server owns confirmation, duplicate-start protection, checkpoints, restart
behavior, state APIs, and event streaming.

Before preparation, the shared lifecycle validates request keys, visible and conditional fields, choice values, positive integer syntax, and defaults. Executors then apply process-specific semantic and fixed-target validation.

`Preflight`, `GetPlan`/`Prepare`, `Simulate`, and `Start` define the custom go-images lifecycle. Do not combine these handlers with `DurableAction`.

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
go run ./cmd/releaseagent serve
```

Authenticate `az` before starting the UI and `gh` before using GitHub-backed go-infra actions. Each
confirmed execution creates a tagged DEVDIV `Issue` under `DevDiv\GoLang`; this includes go-images
test mode and go-infra dry-run mode. Every item receives its process ID as a tag. Test and dry-run
items also receive `test`; all release UI queries combine these tags with `releaseagent`. Azure
DevOps treats tags as case-insensitive and may display the project-canonical casing, such as `Test`.
Simulations and unconfirmed plans remain in memory and create no work item. To restore one
explicitly, add `-release-work-item <id>`. Starting the server does not perform an external action.
Opening the go-infra page performs read-only preflight checks; a mutation still requires preparing
the exact request and confirming it.

Releaseagent owns `System.Description` on these work items. It shows status and release type plus
useful process details. Go-images work items include mode, versions, publication prefix, source
commit, Azure build, and checkpoint timestamps. Generic actions include their intent, action,
reviewed facts, target, and external run. Links open the underlying commit, build, pull request, or
workflow run. The base64url-encoded canonical JSON remains in a collapsed managed-state section so
Azure DevOps HTML normalization cannot alter release state. Add operator notes as work-item comments
rather than editing Description.

The dashboard queries active tagged work items and the ten most recently closed items. Starting and
running work appears under **Ongoing releases**. Failed, canceled, and uncertain work appears under
**Needs attention**, while successful closed work appears under **Recently completed**. Clicking a
card or **Open** selects one work item for this server. Its Azure DevOps browser link appears on the
dashboard and selected release page. **View JSON** exports its snapshot for manual repair and imports
edited JSON only when the exported Azure DevOps revision is still current. Import validates the
process payload and cannot change its process ID or immutable intent digest. Restart without a
selected release before repairing that release's state. Reopening the currently selected work item
is idempotent; selecting a different work item still requires restarting the server.

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

The go-images session document is schema-versioned and structurally fingerprinted. The release UI
stores it in the selected Azure DevOps work item and checks the work item revision on every update.
It contains no credentials.
Schema version 8 stores only standalone go-images input and state. It intentionally rejects the
older full-release-shaped prototype documents rather than retaining a second domain model and
migration path. Workflow revision 8 uses unique step names as graph identity. An incompatible work
item cannot be restored by the current releaseagent.

The current server runs one selected release at a time. Go-images and generic action state live only
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
* Release work items store non-secret input, state, structural and execution digests, and no credentials.

See [ADR 0020: Create UI for release management](https://github.com/microsoft/go-lab/blob/main/docs/adr/0020-microsoft-release-ui-for-go.md) for the accepted local-server design.
