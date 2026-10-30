# Dependency history audits

The shared workflow audits landed Go dependency changes in Git history and publishes a tracker issue only when actionable or unresolved findings remain.
Clean audits write their report to the Actions summary instead of creating an issue.
It does not depend on which tool opened the dependency update.
Maintainers can assign the tracker to Copilot or apply its findings themselves.
The audit does not change code, create branches, or open PRs.

## Setup

Add a scheduled/manual caller for [dependency-audit-target.yml](../../.github/workflows/dependency-audit-target.yml) to the target repository's default branch.
Pin the reusable workflow to a reviewed go-infra commit.

```yaml
name: Dependency history audit

on:
  schedule:
    - cron: "17 8 * * 1"
  workflow_dispatch:
    inputs:
      initial_sha:
        description: "Exclusive starting commit for the first audit, as a full SHA"
        default: ""
        type: string

permissions:
  contents: read

jobs:
  audit:
    uses: microsoft/go-infra/.github/workflows/dependency-audit-target.yml@FULL_GO_INFRA_COMMIT_SHA
    permissions:
      actions: read
      contents: read
      issues: write
      copilot-requests: write
    with:
      initial_sha: ${{ inputs.initial_sha || '' }}
```

Replace `FULL_GO_INFRA_COMMIT_SHA` with the reviewed commit.
Each target runs the shared audit with its own `GITHUB_TOKEN`.
No GitHub App, personal access token, repository enrollment, or shared secrets are required.
The repository must permit the required Actions and Copilot access.
The caller grants a permission ceiling because reusable workflows cannot elevate its token permissions.
The shared wrapper inherits that ceiling, and the generated agent workflow narrows permissions for preparation, read-only analysis, and issue publishing.
Caller workflows may run from other branches or in forked repositories.
The audit still reads a captured revision of that repository's default branch.

## Run an audit

The first audit requires an explicit, exclusive starting commit on the target's default-branch history:

```bash
gh workflow run dependency-audit.yml --repo OWNER/REPOSITORY \
  -f initial_sha=FULL_STARTING_COMMIT_SHA
```

Replace `FULL_STARTING_COMMIT_SHA` with a lowercase, 40-character commit SHA.
Once a checkpoint exists, including one created during a pilot, leave `initial_sha` empty:

```bash
gh workflow run dependency-audit.yml --repo OWNER/REPOSITORY
```

Supplying another starting SHA fails rather than resetting progress.
Scheduled runs resume from the existing checkpoint.

The [checkpoint helper](../../cmd/dependencyaudit/main.go) reads the checkpoint artifact from the previous successful run of the same caller workflow and captures an immutable default-branch head.
It also recognizes existing Actions-bot-owned tracker checkpoints, including closed issues, so repositories can adopt the new behavior without resetting progress.
The [audit agent](../../.github/workflows/dependency-audit-agent.md) reads complete first-parent Go manifest/checksum patches and the current callers without executing target code.
It includes intermediate landed upgrades and reversions, checks versioned upstream sources, and reports what it could not verify.

Reports contain Summary, Inventory, Areas, and Notes sections, plus Resolved areas when needed.
Each area identifies its owning module, affected files and symbols, upstream sources, migration steps, behavior changes, and validation commands.
Every previous unresolved area must be carried forward or explicitly resolved.

The trusted helper saves a versioned `dependency-audit-checkpoint` artifact for each validated report.
Artifacts from failed, cancelled, or in-progress runs never advance the next audit's checkpoint.
Checkpoint ordering follows upload and audit times rather than numeric run IDs, so rerunning an older workflow does not rewind progress.
Actionable tracker issues also retain the visible `dependency-audit-v1` fenced JSON checkpoint and instructions for applying the audit.
The model cannot choose the checkpoint or request arbitrary issue updates.
A new revision with findings creates a new tracker and closes older trackers.
When no findings remain, the workflow writes the full report to its summary and closes the previous tracker without creating a replacement.
Repeating the same revision replaces the existing report and recovers interrupted publication without resetting progress.
Findings that reappear after a clean audit at the same revision reopen the matching tracker.
No database or separate state branch is used.
Checkpoint artifacts follow the repository's configured Actions artifact retention.
Keep that retention longer than the audit schedule interval.
Each successful audit refreshes the checkpoint, including clean audits.
If artifacts expire or are deleted, the helper can resume from an existing trusted tracker checkpoint and reports that fallback.
If no usable checkpoint remains, the audit fails and requires an explicit starting SHA rather than silently resetting progress.
The agent workflow prepares its own checkpoint and passes the trusted context to analysis and publication through job outputs, without a separate context artifact.
Rewritten history, corrupt metadata, and changed default branches fail explicitly.

Go declarations are not a resolved dependency graph.
Transitive impacts, workspace/platform/build-tag coverage, private replacements, runtime behavior, and non-Go ecosystems may remain unverified.
Blocked documentation is a limitation, not evidence of compatibility.
Add required documentation hosts to the agent's network allowlist and recompile.

## Apply findings

Select an open area from the tracker and assign the issue to Copilot, or implement that area yourself.
The issue includes the investigation and application instructions; there is no separate apply workflow.

Start from the current default branch and recheck that the finding still applies.
Check for an existing PR before making changes.
Keep the change focused on the selected area, preserve behavior except for documented migration requirements, and add regression tests.
Run the area's validation commands and the repository's documented checks in each affected module.
Report checks that could not be run.

Open a draft PR that references the tracker and area slug without closing keywords such as "Fixes" or "Closes".
Do not approve or merge the PR automatically.
A PR or checked box is not proof of resolution; a later audit verifies the merged code.

## Develop the workflow

Edit the agent Markdown source and regenerate its lock file with the repository's pinned gh-aw compiler:

```bash
gh aw compile dependency-audit-agent --strict
go test -race ./cmd/dependencyaudit
```

Keep the artifact-download action override in [.github/aw/actions-lock.json](../../.github/aw/actions-lock.json) when regenerating the lock.
The helper tests cover issue and successful-run checkpoint recovery, clean bootstrap and repeated audits, carry-forward and resolution, retry replacement after clean runs, expired or corrupt artifacts, history changes, native output metadata, and application instructions.
