# Audit Go dependency upgrades

Use the shared workflow to review landed Go dependency updates and find changes your code needs.

## Add the workflow

Your repository needs GitHub Actions and Copilot access.
Save this caller as `.github/workflows/dependency-audit.yml` on the repository's default branch.
Replace `FULL_GO_INFRA_COMMIT_SHA` with the full commit SHA of a [tagged go-infra release](https://github.com/microsoft/go-infra/tags) that includes this workflow.
If the workflow has not been released yet, use the commit SHA from its merged PR.

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

Choose a commit on the default branch immediately before the dependency updates you want reviewed.
The audit excludes that starting commit.
The ending commit is the latest commit on the default branch when the audit starts.
Replace `OWNER/REPOSITORY` with your repository.
Use the commit's full, lowercase, 40-character SHA for `FULL_STARTING_COMMIT_SHA`.

```bash
gh workflow run dependency-audit.yml --repo OWNER/REPOSITORY \
	-f initial_sha=FULL_STARTING_COMMIT_SHA
```

## Run later audits

Leave `initial_sha` empty for later audits:

```bash
gh workflow run dependency-audit.yml --repo OWNER/REPOSITORY
```

Manual and scheduled audits both resume from the latest saved progress.
A successful run with a clean threat-detection verdict saves new progress, even when it reports no findings.
Supplying `initial_sha` while a checkpoint exists fails rather than resetting progress.

## Act on findings

Use tracker issues to follow up on audit findings.
You do not need to inspect every successful run.
If a run lists no actionable or unresolved findings, its report is available in the run summary instead of a new issue.
A run with no findings also closes the previous tracker.

When you act on a tracker, read its findings and the Notes section.
Notes identify gaps in the audit, such as unavailable upstream documentation or dependency behavior the audit could not verify.
The audit does not run your repository's code or tests.

Assign the tracker to Copilot, or follow its implementation and validation instructions yourself.
You can address the whole tracker in one PR if the changes belong together.
Open a draft PR that references the tracker and the findings it addresses.

Closing a tracker does not mark its findings as resolved for the audit.
The next audit rechecks the findings and reopens the tracker or creates a new one if it still finds work.

## Recover a missing checkpoint

Keep Actions artifact retention longer than the interval between audits.
Successful runs with a clean threat-detection verdict save a `dependency-audit-checkpoint` artifact for the next run.
If the artifact is unavailable, the workflow automatically resumes from its latest audit tracker issue, including a closed issue.

Check the failed step's log for `no checkpoint exists` or `previous audit checkpoint is no longer available`.
For either error, [repeat the first-audit steps](#run-the-first-audit) with an explicit starting SHA.
