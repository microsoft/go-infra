# upstreamreview

`upstreamreview` finds changes to upstream Go importers. Install the binary with `go install github.com/microsoft/go-infra/cmd/upstreamreview@VERSION`, or pin it in a consuming module with `go get -tool github.com/microsoft/go-infra/cmd/upstreamreview@VERSION`.

## importdiff

Compare two local Git revisions and list changed Go files that directly import a selected package or subpackage on either side of the change, including tests and build-tagged files. No GitHub token is needed.

```sh
upstreamreview importdiff -repo /path/to/golang/go -exclude src/crypto/internal/fips140 OLD_SHA NEW_SHA
```

By default, this subcommand searches `src` for `crypto/internal/fips140` importers and excludes nothing. Repeat `-src`, `-import`, or `-exclude` to select multiple trees. Each flag replaces its own default on first use. The JSON array contains paths, old/new Git blob IDs, and matched imports on both sides; `[]` means no matches.

## check-pr-imports

Check a GitHub.com pull request from a checkout of its test merge with `fetch-depth: 2`. The command is read-only by default: it needs a `GH_TOKEN` with read access to pull requests and prints a Markdown summary with links to matching files and a reproduction command. For example:

```sh
go tool upstreamreview check-pr-imports -repo "$GITHUB_REPOSITORY" -pr "$PR" -merge "$GITHUB_SHA" \
  -upstream golang/go -submodule go -label upstream-crypto-reviewed \
  -src src -import crypto/internal/fips140 -exclude src/crypto/internal/fips140 \
  >> "$GITHUB_STEP_SUMMARY"
```

`-repo` is the repository that owns the PR and label; `-upstream` is the GitHub `owner/name` used to fetch source and build diff links. Set it in the workflow. `-submodule` is a path relative to the target repository root. Supply at least one `-src` and one `-import`; both are repeatable, as is the optional `-exclude`. Source and excluded directories are relative to the upstream repository root.

The check passes if the scan is clean or the PR label acknowledges the exact pair of upstream pins in the base and proposed merge. Enable the `labeled` and `unlabeled` PR activities: the label's own `labeled` event (`GITHUB_EVENT_PATH`) establishes a new acknowledgment. Other runs can reuse it only when the bot's summary comment records both pins and the same label application from the PR's issue history. Changing either pin or removing the label invalidates that acknowledgment. A label with no verifiable record (for example, if the comment was deleted or could not be posted) does not acknowledge a new scan. Remove and reapply it after review.

If the PR head moved, see the checks for the new head. If only the base moved, update the PR branch to start a fresh check; retrying the original job uses the same commit.

### Opt in to PR comments

In Actions, add `-update-pr-comment` to the same command to publish the verdict in one bot comment, record pin acknowledgments for future runs, and remove stale review labels. Give the same-repository job `pull-requests: write` and serialize runs per PR. Do not execute code from a fork in a write-enabled job.

The comment links to the scanned PR head, base, and exact workflow attempt. A missing acknowledgment still gets a comment before the check exits unsuccessfully. A clean scan updates an existing comment without creating one; if there are no conversation comments or labels to clear, it skips publication entirely. A failed or stale comparison never posts. The checker rechecks PR and label freshness before writing, and logs comment or label cleanup errors without changing the scan result.
