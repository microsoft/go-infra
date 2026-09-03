---
description: Classifies a pull request by every substantive change kind
tracker-id: classify-pull-request-kind
on:
   roles: all
   workflow_call:
      inputs:
         pr_number:
            description: "Pull request number to classify"
            required: true
            type: number
   workflow_dispatch:
      inputs:
         pr_number:
            description: "Pull request number to classify"
            required: true
            type: number
concurrency:
   group: "gh-aw-${{ github.workflow }}-${{ github.repository }}-${{ inputs.pr_number }}"
   cancel-in-progress: true
permissions:
   contents: read
   issues: read
   pull-requests: read
   copilot-requests: write
inlined-imports: true
network: defaults
tools:
   cli-proxy: true
   github:
      mode: gh-proxy
      toolsets: [default]
      min-integrity: unapproved
safe-outputs:
   noop:
      report-as-issue: false
   add-labels:
      allowed:
         - kind:code
         - kind:docs
         - kind:tests
         - kind:examples
         - kind:dependencies
         - kind:ci
         - failed-auto-classify
      # A PR may have any number of kinds, but a kind and the failure label are never applied simultaneously.
      max: 6
      target: "${{ inputs.pr_number }}"
   remove-labels:
      allowed:
         - kind:code
         - kind:docs
         - kind:tests
         - kind:examples
         - kind:dependencies
         - kind:ci
         - failed-auto-classify
      # A PR edited by a dev may have every kind and also the failure label.
      # The classifier may correctly decide a PR in that state has no kind, and all labels must be removed.
      max: 7
      target: "${{ inputs.pr_number }}"
timeout-minutes: 10
---

# Pull Request Kind Classifier

Classify pull request `${{ inputs.pr_number }}` in `${{ github.repository }}` and maintain every applicable `kind:*` label.

## Allowed Kinds

Choose every kind represented by a substantive part of the change. Most PRs will have one or two kinds; a PR may have more.

- `kind:dependencies`: dependency versions, module manifests, checksums, lock files, or dependency-bot maintenance change.
- `kind:ci`: CI, build, repository automation, or GitHub workflows change.
- `kind:docs`: documentation, guides, API documentation, or comments change substantively.
- `kind:tests`: tests, fixtures, test data, or test infrastructure change.
- `kind:examples`: samples, demos, or metadata used to discover or run examples change.
- `kind:code`: production behavior, public API, bug fixes, features, or refactors change.

## Process

1. Use GitHub tools to read the PR title, body, author, existing labels, changed files, and relevant diff patches.
2. Determine the complete set of applicable kinds from the definitions above. Prefer the actual diff over the title. Do not add a kind merely because a generated file mentions it.
3. On successful classification, remove allowed `kind:*` labels that are no longer applicable and remove `failed-auto-classify` if it is present. Do not remove labels outside the allowed set.
4. Add all applicable kind labels that are not already present in one `add_labels` call.
5. Do not add comments, reviews, or any labels outside the allowed set.
6. If the PR cannot be read or classified confidently, leave existing kind labels unchanged and add `failed-auto-classify` so maintainers can detect the problem. A hard workflow failure is also visible as a failed check.
7. If classification succeeds and no label changes are needed, use `noop` rather than making an unnecessary write.
