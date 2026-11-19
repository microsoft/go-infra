# upstreamreview

`upstreamreview` finds changes to upstream Go importers. Install the binary with `go install github.com/microsoft/go-infra/cmd/upstreamreview@VERSION`, or pin it in a consuming module with `go get -tool github.com/microsoft/go-infra/cmd/upstreamreview@VERSION`.

## importdiff

Compare two local Git revisions and list changed Go files that directly import a selected package or subpackage on either side of the change, including tests and build-tagged files. No GitHub token is needed.

```sh
upstreamreview importdiff -repo /path/to/golang/go -exclude src/crypto/internal/fips140 OLD_SHA NEW_SHA
```

By default, this subcommand searches `src` for `crypto/internal/fips140` importers and excludes nothing. Repeat `-src`, `-import`, or `-exclude` to select multiple trees. Each flag replaces its own default on first use. The JSON array contains paths, old/new Git blob IDs, and matched imports on both sides; `[]` means no matches.

## check-pr-imports

Check a GitHub.com pull request from a checkout of its test merge with `fetch-depth: 2` and a `GH_TOKEN` with read access to pull requests. The Markdown summary links to each matching file and gives the equivalent `upstreamreview importdiff` command. For example:

```sh
go tool upstreamreview check-pr-imports -repo "$GITHUB_REPOSITORY" -pr "$PR" -merge "$GITHUB_SHA" \
  -upstream golang/go -submodule go -label upstream-crypto-reviewed \
  -src src -import crypto/internal/fips140 -exclude src/crypto/internal/fips140 \
  >> "$GITHUB_STEP_SUMMARY"
```

`-repo` is the repository that owns the PR and label; `-upstream` is the GitHub `owner/name` used to fetch source and build diff links. Set it in the workflow. `-submodule` is a path relative to the target repository root. Supply at least one `-src` and one `-import`; both are repeatable, as is the optional `-exclude`. Source and excluded directories are relative to the upstream repository root.

The check passes if the scan is clean or the live PR has the configured label. The label applies to the whole PR, including later commits. Enable the `labeled` and `unlabeled` PR activities so adding or removing it reruns the check. A failed scan or a checkout that no longer matches the live PR fails even if the label is present.

In Actions, a verified scan sets the `review` step output to a JSON object with the `verdict` (`clean`, `review-needed`, or `acknowledged`), the full `base` and `head` SHAs it verified, and the workflow run `attempt`. `review-needed` is emitted even though the check fails for missing acknowledgment. Failed and stale scans emit no verdict. If the PR head moved, see the checks for the new head. If only the base moved, update the PR branch to start a fresh check; retrying the original job uses the same commit.

## update-pr-comment

`upstreamreview update-pr-comment -review "$REVIEW"` publishes the verdict from `check-pr-imports` without re-evaluating the PR labels. Pass the same `-label` for the label instructions. The comment links to the scanned PR head and base commits and to the exact workflow attempt. A `clean` verdict updates an existing comment to **no matching changes** without creating a new one. The writer reads the PR from `GITHUB_EVENT_PATH` and the run ID from `GITHUB_RUN_ID`; it skips the write if the PR is closed, either revision changed, or `GH_TOKEN` is read-only.

Run this in a separate `pull-requests: write` job whenever `check-pr-imports` sets `review`, even if the review job fails for lack of acknowledgment. Pass that output through a `REVIEW` environment variable. To use the tool pin from the PR checkout, restrict that job to branches in the target repository and run `go tool upstreamreview update-pr-comment -review "$REVIEW" -label "$REVIEW_LABEL"` from the declaring module. To handle fork PRs, run a trusted, independently pinned copy without checking out PR code instead.
