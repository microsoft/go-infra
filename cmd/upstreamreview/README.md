# upstreamreview

`upstreamreview` helps maintainers of a Go fork review upstream updates. It finds changed Go files that directly import a package of interest or one of its subpackages in either revision. This catches removed imports and edits that leave the imports unchanged, including in tests and build-tagged files.

Install with `go install github.com/microsoft/go-infra/cmd/upstreamreview@VERSION`, or pin it with `go get -tool github.com/microsoft/go-infra/cmd/upstreamreview@VERSION` and run it as `go tool upstreamreview`.

## importdiff

Compare two commits in a local clone. No GitHub token is needed:

```sh
upstreamreview importdiff -repo /path/to/golang/go -exclude src/crypto/internal/fips140 OLD_SHA NEW_SHA
```

The JSON output lists each matching path, its old and new Git blob IDs, and the matching imports on each side. An empty result is `[]`.

The defaults are `-src src -import crypto/internal/fips140` with no exclusions. Each of `-src`, `-import`, and `-exclude` can be repeated; the first explicit value replaces that flag's default. Source and exclusion paths are relative to the repository root, even when running from a subdirectory.

## check-pr-imports

Run from the checkout of a GitHub PR's test merge (`fetch-depth: 2`). The checker compares the submodule pin in the merge's first parent with the pin in the merge itself. It prints a Markdown summary with file links and a command to reproduce the comparison.

In a `pull_request` workflow, set `GH_TOKEN` to `${{ github.token }}` with `pull-requests: read` (and `contents: read` for checkout), and `PR_NUMBER` to `${{ github.event.number }}`:

```sh
go tool upstreamreview check-pr-imports -repo "$GITHUB_REPOSITORY" -pr "$PR_NUMBER" -merge "$GITHUB_SHA" \
  -upstream golang/go -submodule go -label upstream-crypto-reviewed \
  -src src -import crypto/internal/fips140 -exclude src/crypto/internal/fips140 \
  >> "$GITHUB_STEP_SUMMARY"
```

`-repo` owns the PR and label; `-upstream` owns the submodule source. Supply at least one `-src` and one `-import`; `-exclude` is optional. Directory paths are relative to the upstream root except `-submodule`, which is relative to the PR repository root.

The check passes if there are no matches or the label acknowledges **both upstream pins**. Only the label's own `labeled` event establishes a new acknowledgment. Later runs require a record of the same pins and label application in the bot's comment. A read-only run can verify an acknowledgment but cannot save it for later runs.

### Opt in to PR comments

Add `-update-pr-comment` to the command to save acknowledgments, update one bot comment, and remove stale labels. Enable `labeled` and `unlabeled` alongside the usual PR events and serialize runs per PR. Give `pull-requests: write` only to a trusted same-repository job; do not run fork code with write access.

The comment links to the scanned PR commits and run attempt, even when review is still needed. Clean scans update an existing comment but never create one. Failed or stale comparisons do not publish. If a write fails, the summary warns and the step log gives the error; the scan verdict does not change.

**Troubleshooting:**

- **Review still required after labeling:** A canceled label run or failed write may not have saved the acknowledgment. Deleting the bot comment also removes the record. Let the latest run finish, review the diff, and reapply the label (remove it first if still present).
- **Stale scan:** If the PR head moved, use the new head's checks. If only the base moved, update the PR branch; retrying the old job uses the original merge commit.
