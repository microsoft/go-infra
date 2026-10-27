# Dependency history audits

The shared workflows audit landed Go dependency changes in Git history and provide a separate manual workflow for applying findings.
Runs can execute centrally in go-infra or through thin callers in target repositories.
The analysis does not depend on which tool opened the dependency update.

## Setup

Choose one scheduler per target.
Do not both schedule a repository-local caller and enroll that repository in the central schedule.

| Setting | Value |
| --- | --- |
| `DEPENDENCY_AUDIT_REPOSITORIES` variable | Central runs only: a JSON array of enrolled `owner/repo` names. Repository-local callers can target only themselves. |
| `app_id` input or `DEPENDENCY_AUDIT_APP_ID` variable | Optional GitHub App client ID for local apply; required for cross-repository runs. |
| `DEPENDENCY_AUDIT_APP_PRIVATE_KEY` secret | The configured App's PEM private key. Map an existing secret to this name rather than copying the key. |
| `DEPENDENCY_AUDIT_ENABLE_APPLY` variable | `true` to enable manual migrations in the repository running the workflow. Leave unset for audit-only use. |

### Repository-local callers

Add a scheduled/manual caller for [dependency-audit-target.yml](../../.github/workflows/dependency-audit-target.yml).
Set `target_repo` to `${{ github.repository }}` and pass the manual `initial_sha` input.
Pin the cross-repository `uses` reference to a reviewed go-infra commit.

Local audits use `GITHUB_TOKEN` and need no App or personal access token.
The audit caller grants `actions: write`, `contents: read`, `issues: write`, `pull-requests: read`, and `copilot-requests: write`.

A manual apply caller uses [dependency-audit-apply.yml](../../.github/workflows/dependency-audit-apply.yml) with `target_repo`, `area`, and optional `issue` and `replace_pr` inputs.
Its permission ceiling also includes `contents: write` and `pull-requests: write`.
Preparation and model jobs request narrower permissions within that ceiling.
The repository must permit the required Actions and Copilot access.

App configuration changes the final apply result:

| Configuration | Result |
| --- | --- |
| No App client ID or key | Publish a validated branch and an Open a PR link on the tracker. A maintainer opens the PR. |
| App client ID and key | Open a fresh draft PR automatically, allowing normal PR CI to trigger. |

For manual PR links, leave both the App client ID and private key unconfigured.
For apply, providing only one fails explicitly.
Local audits always use `GITHUB_TOKEN`, even when an App is configured for apply.

### Central runs

Configure enrollment and App settings in go-infra.
Install the App only on enrolled targets.
Audits need Contents read and Issues write; apply additionally needs Contents write and Pull requests write.
Minted tokens are short-lived and scoped to the selected target and operation.

Central runs do not need trigger files in target repositories.
The public central repository cannot audit private targets; use a private repository-local caller for those.
Fork and non-default-branch entrypoints fail before target access is granted.

The model has read-only repository access.
Publishing credentials stay in trusted publication and reporting steps, and target test containers receive no credentials.
The App key is used by pinned token-minting actions and native post-run log redaction, not by model tools or target tests.
No personal authentication bridge is needed.

## Audit

[dependency-audit.yml](../../.github/workflows/dependency-audit.yml) runs centrally on Monday at 08:17 UTC, or manually.
The first audit of each target requires an explicit, exclusive starting commit on its default-branch history:

```bash
gh workflow run dependency-audit.yml --repo microsoft/go-infra \
  -f target_repo=microsoft/agent-framework-go \
  -f initial_sha=FULL_STARTING_COMMIT_SHA
```

Replace `FULL_STARTING_COMMIT_SHA` with a lowercase, 40-character commit SHA.
Later central runs omit `initial_sha`.
Omit `target_repo` to audit every enrolled repository; a first-run SHA can only be supplied for one target at a time.

A repository-local caller named `dependency-audit.yml` can be started without a target input:

```bash
gh workflow run dependency-audit.yml --repo OWNER/REPOSITORY \
  -f initial_sha=FULL_STARTING_COMMIT_SHA
```

Once a checkpoint exists, including one created during a pilot, leave `initial_sha` empty.
Supplying another starting SHA fails rather than resetting progress.
Caller workflows must be available on the caller's default branch.

The [checkpoint helper](../../cmd/dependencyaudit/main.go) reads the latest automation-owned tracker and captures an immutable default-branch head.
The [audit agent](../../.github/workflows/dependency-audit-agent.md) reads complete first-parent Go manifest/checksum patches and the current callers.
It includes intermediate landed upgrades and reversions, checks versioned upstream sources, and reports what it could not verify.

Reports contain Summary, Inventory, Areas, and Notes sections, plus Resolved areas when needed.
Each area has a stable slug and a Module entry containing its backtick-quoted `go.mod` path.
Every previous unresolved area must be carried forward or explicitly resolved.

The trusted helper attaches a visible `dependency-audit-v1` fenced JSON checkpoint.
The model cannot choose the checkpoint or request arbitrary issue updates.
A new revision creates a new tracker and closes older trackers.
Repeating the same revision refreshes the existing report and recovers interrupted publication without resetting progress.

The tracker stores audit progress; migration attempts are tracked through branches, PRs, and managed tracker comments.
No database or separate state branch is used.
Prepared context artifacts expire after one day.
Rewritten history, corrupt metadata, and changed default branches fail explicitly.

Go declarations are not a resolved dependency graph.
Transitive impacts, workspace/platform/build-tag coverage, private replacements, runtime behavior, and non-Go ecosystems may remain unverified.
Blocked documentation is a limitation, not evidence of compatibility.
Add required documentation hosts to both agent network allowlists and recompile.

## Apply one area

Select an area from the current open tracker.
For a central run:

```bash
gh workflow run dependency-audit-apply.yml --repo microsoft/go-infra \
  -f target_repo=microsoft/agent-framework-go -f area=STABLE_AREA_SLUG
```

A repository-local apply caller supplies its own target, so omit `target_repo` when dispatching that caller.
The optional `issue` input asserts the current tracker number.

If a migration PR or validated branch is already pending for the area, apply refreshes its tracker link without running the model or tests again.
Human-opened PRs from generated branches are recognized too.
Open draft, ready-for-review, and maintainer-edited PRs are left untouched.
Ambiguous or modified pending branches fail explicitly instead of being overwritten.

Otherwise, the tracker must match the current default-branch head.
A fresh migration starts at that audited commit.
Apply never resumes an old fix branch, rebases it, or updates its code.

To replace a previous attempt, manually close its unmerged PR and pass its number as `replace_pr`:

```bash
gh workflow run dependency-audit-apply.yml --repo microsoft/go-infra \
  -f target_repo=microsoft/agent-framework-go -f area=STABLE_AREA_SLUG \
  -f replace_pr=123
```

The new attempt and tracker comment link the previous PR.
The workflow never closes a migration PR automatically or replaces a merged PR.
If another PR or branch appears during generation, publication stops; the next run discovers the existing attempt rather than creating a duplicate.

The [apply agent](../../.github/workflows/dependency-audit-apply-agent.md) edits Go source directly.
An independent job applies the native patch, checks its module and file limits, and verifies that any native bundle has the same result tree.
Module download, formatting, vet, tests, and build run in a pinned Go 1.26 Docker image without credentials.
Only the download container has network access.
Publication requires successful validation.

With an App, native gh-aw publishing opens a draft PR.
Without one, the trusted [manual publisher](../../cmd/dependencyaudit/migration.go) verifies the exact validated tree before creating a new branch ref and reporting an Open a PR link.
It never updates an existing ref.
Retries reuse published attempts and the managed tracker comment after partial publication.

Migrations are limited to 20 regular Go files in one module and a 256 KB patch.
Manifests, checksums, workflows, hidden/vendor/third-party paths, and unrelated modules cannot change.
New dependencies and unsupported migrations need human attention.
Nothing is automatically approved or merged.
Apply does not advance the audit checkpoint or resolve an area; a later audit checks the merged code.

## Local checks

```bash
go test -race ./cmd/dependencyaudit
go build -o /dev/null ./cmd/dependencyaudit
gh aw compile dependency-audit-agent dependency-audit-apply-agent --strict
```

Compile generated lock workflows rather than editing them.
Keep `inlined-imports: true` in both agent sources so reusable callers do not need the source Markdown in their own checkout.
Stage source, generated locks, and the action lock together when those files change.

The Actions runner used for migration validation needs Docker.
The helper's Go tests use mocked GitHub APIs and temporary Git repositories, so they need neither Docker nor App credentials.
Live auditing and apply publication require separate verification.
