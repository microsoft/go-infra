# Dependency history audits

The central workflows audit dependency changes in Git history and provide a
separate manual workflow for applying findings. Native gh-aw outputs publish
reports and fresh draft PRs. Existing PRs are linked without changing their code.

## Setup

Configure these settings in the central go-infra repository:

| Setting | Value |
| --- | --- |
| `DEPENDENCY_AUDIT_REPOSITORIES` variable | JSON array of enrolled `owner/repo` names. |
| `DEPENDENCY_AUDIT_APP_ID` variable | Team-controlled GitHub App client ID. |
| `DEPENDENCY_AUDIT_APP_PRIVATE_KEY` secret | That App's PEM private key. |
| `DEPENDENCY_AUDIT_ENABLE_APPLY` variable | `true` to enable manual migrations. Leave unset for audit-only use. |

Install the App only on enrolled repositories. Audits need Contents read and
Issues write. Apply additionally needs Contents write and Pull requests write.
Tokens are short-lived and scoped to one selected repository. Model jobs have
read-only repository permissions; publishing credentials stay in native output
jobs. The caller grants the permission ceiling required by the reusable native
workflows, while preparation and model jobs explicitly downgrade their scopes.

The App key is used by pinned token-minting actions and gh-aw's post-run log
redaction, not passed to the model or target tests. No personal authentication
bridge is needed. Private targets require a private central deployment. Fork and
non-default-branch entrypoints fail before target access is granted.

## Audit

[dependency-audit.yml](../../.github/workflows/dependency-audit.yml) runs weekly
on Monday at 08:17 UTC, or manually. The first audit of each repository requires
an explicit, exclusive starting commit on its default-branch history:

```bash
gh workflow run dependency-audit.yml --repo microsoft/go-infra \
  -f target_repo=microsoft/agent-framework-go \
  -f initial_sha=<40-character-starting-commit>
```

Later runs omit `initial_sha`. Omit `target_repo` to audit all enrolled repos.
No workflow or Dependabot-specific trigger is needed in target repositories.

The [checkpoint helper](../../cmd/dependencyaudit/main.go) reads the latest
automation-owned tracker, captures the default-branch head, and prepares context.
The [audit agent](../../.github/workflows/dependency-audit-agent.md) reads complete
first-parent Go manifest/checksum patches and current callers directly. It
checks versioned upstream sources and reports uncertainty. There is no custom
dependency parser, import inventory, or JSON findings protocol.

Reports have Summary, Inventory, Areas, Resolved areas, and Notes
sections. Each area has a stable slug and `- Module: ` followed by its backtick-
quoted `go.mod` path. The trusted helper requires every previous unresolved slug
to remain open or have an explicit resolution, and attaches a hidden checkpoint.
Headings inside code examples are ignored when validating the report.
The model cannot choose the checkpoint or request arbitrary issue updates.

A new revision creates a new tracker and closes older trackers. Repeating the
same revision refreshes the existing issue and finishes any interrupted closure.
Native title deduplication is an additional safeguard. Failures do not advance
the checkpoint. The issue is the only durable state store; context artifacts
expire after one day. Rewritten history, corrupt metadata, and changed default
branches fail explicitly instead of resetting progress.

Go declarations are not a resolved dependency graph. Transitive impacts,
workspace/platform/build-tag coverage, private replacements, runtime behavior,
and non-Go ecosystems may remain unverified. Blocked upstream documentation is a
limitation, not evidence of compatibility. Add required documentation hosts to
both agent network allowlists and recompile.

## Apply one area

Use [dependency-audit-apply.yml](../../.github/workflows/dependency-audit-apply.yml):

```bash
gh workflow run dependency-audit-apply.yml --repo microsoft/go-infra \
  -f target_repo=microsoft/agent-framework-go -f area=<stable-slug>
```

The optional `issue` input asserts the current tracker number. The tracker must
be open and include the selected area. If a managed PR for that area is already
open, apply refreshes its tracker link and stops without running the model or
tests. Draft, ready-for-review, and maintainer-edited branches are left untouched.
The link-only step uses a separate token without Contents write or Pull requests
write access. It runs during preparation, without another runner or context
artifact.

Otherwise, a fresh draft starts at the captured audited default-branch commit.
The tracker must be current with that branch. Apply never resumes an old fix
branch, rebases it, or updates its code. A rerun after partial publication finds
the existing PR and refreshes the same managed tracker comment.

Replacing a previous attempt is explicit. Close the old, unmerged PR manually,
then pass its number as `replace_pr`:

```bash
gh workflow run dependency-audit-apply.yml --repo microsoft/go-infra \
  -f target_repo=microsoft/agent-framework-go -f area=<stable-slug> -f replace_pr=123
```

The new draft and tracker comment link the previous PR. The workflow never
closes an old PR automatically or replaces a merged PR. Open PRs take priority
over new creation; retries of an already-created replacement only refresh its
link. If a PR appears while the agent is generating a fix, publication stops
and the next run links it instead of creating a duplicate.

The [apply agent](../../.github/workflows/dependency-audit-apply-agent.md) edits
Go source directly. An independent job applies the native patch, checks its
module and file limits, and verifies any native bundle has exactly the same
result tree. Formatting, module download, vet, tests, and build run in a pinned
Docker image without credentials. Only module download has network access.
All native publishing is gated on that job's success.

Native limits replace the earlier custom replacement-file format: at most 20 Go
files and a 256 KB patch. Manifests, checksums, workflows, hidden/vendor
paths, and unrelated modules cannot be changed. New dependencies and unsupported
migrations require human attention. Nothing is automatically approved or merged.
Apply does not advance the checkpoint or resolve an area; a later audit checks
the merged code. A failed native publication may leave an unused branch, but a
retry checks for an existing PR before requesting another.

## Local checks

```bash
go test -race ./cmd/dependencyaudit
gh aw compile dependency-audit-agent dependency-audit-apply-agent --strict
```

Compile generated lock workflows rather than editing them. Stage source,
generated locks, and the action lock together. Docker and a configured GitHub
App are required for live migration execution; helper and patch/bundle regression
tests do not need either.
