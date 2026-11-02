# Audit Go dependency upgrades

Use the shared workflow to review landed Go dependency updates and find changes your code needs.

## Add the workflow

Your repository needs GitHub Actions and Copilot access.
Save this caller as `.github/workflows/dependency-audit.yml` on the repository's default branch.
Replace `FULL_GO_INFRA_COMMIT_SHA` with a reviewed go-infra commit.

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

The workflow uses the repository's `GITHUB_TOKEN` without extra credentials.
Keep the permissions block, since the [reusable workflow](../../.github/workflows/dependency-audit-target.yml) cannot add permissions omitted by its caller.

## Run the first audit

If an audit checkpoint already exists, [run without `initial_sha`](#run-later-audits) instead.

Choose a commit on the default branch immediately before the dependency updates you want reviewed.
The audit excludes that starting commit and ends at a captured default-branch revision.
Replace `OWNER/REPOSITORY` with your repository.
Use the commit's full, lowercase, 40-character SHA for `FULL_STARTING_COMMIT_SHA`.

```bash
gh workflow run dependency-audit.yml --repo OWNER/REPOSITORY \
	-f initial_sha=FULL_STARTING_COMMIT_SHA
```

## Run later audits

Leave `initial_sha` empty to resume from the saved checkpoint:

```bash
gh workflow run dependency-audit.yml --repo OWNER/REPOSITORY
```

The weekly schedule resumes from the same checkpoint.
Supplying `initial_sha` while a checkpoint exists fails rather than resetting progress.

## Act on the result

Open the completed run to see the result.
If no actionable or unresolved findings remain, read the run summary.
With no findings left, the workflow closes the previous tracker without creating a new issue.

Read the report's Notes for unverified behavior or inaccessible documentation.
The audit does not run repository code or tests.

If the run links a tracker issue with findings, use it to plan the changes:

1. Select an area from the tracker.
   Check the current default branch and existing PRs before making changes.
2. Assign the tracker to Copilot, or follow the issue's implementation and validation instructions yourself.
3. Open a draft PR that references the tracker and identifies the finding it addresses.
   Do not use closing keywords such as `Fixes` or `Closes` so GitHub does not automatically close the tracker.

After the PR merges, the next audit rechecks the finding and reports whether it is resolved.

## Recover a missing checkpoint

Keep Actions artifact retention longer than the interval between audits.
Each successful audit saves a `dependency-audit-checkpoint` artifact for the next run.
If the artifact is unavailable, the workflow can resume from a tracker issue, including a closed issue.

If no usable checkpoint remains, choose a commit before the dependency updates you need to recheck.
Rerun the first-audit command with that SHA as `initial_sha`.
