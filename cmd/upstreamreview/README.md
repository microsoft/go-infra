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

The check passes if the scan is clean or the PR label acknowledges the exact pair of upstream pins in the base and proposed merge. Enable the `labeled` and `unlabeled` PR activities: the label's own `labeled` event (`GITHUB_EVENT_PATH`) establishes a new acknowledgment. Other runs can reuse it only when the bot's summary comment records both pins and the same label application from the PR's issue history. Changing either pin or removing the label invalidates that acknowledgment. A label with no verifiable record (for example, if the comment was deleted or could not be posted) does not acknowledge a new scan. Remove and reapply it after review.

In Actions, a verified scan sets `review` to JSON with the `verdict` (`clean`, `review-needed`, or `acknowledged`), the outer PR `base` and `head`, the upstream `old` and `new` pins, the workflow run `attempt`, and the active `labelEvent` ID when applicable. It also sets `update-comment`: `false` for a clean PR known to have no conversation comments or label to clear. `review-needed` is emitted even though the check fails for missing acknowledgment. Failed and stale scans emit no verdict. If the PR head moved, see the checks for the new head. If only the base moved, update the PR branch to start a fresh check; retrying the original job uses the same commit.

## update-pr-comment

`upstreamreview update-pr-comment -review "$REVIEW" -label "$REVIEW_LABEL"` publishes the verified verdict. Pass the same label as the checker. The comment links to the scanned PR head and base commits and to the exact workflow attempt. For `acknowledged`, it also records the reviewed pins and label application. For a stale label, the writer removes only the application identified by the scan. A `clean` verdict updates an existing comment to **no matching changes** without creating a new one. The writer reads the PR from `GITHUB_EVENT_PATH` and the run ID from `GITHUB_RUN_ID`, and checks that the PR and label are still current before writing.

On same-repository PRs the writer can be a final step in the scan job, sharing checkout and Go setup. Give only that job `pull-requests: write`, serialize runs per PR, and pass the scan output through a `REVIEW` environment variable. Run the step with `if: ${{ !cancelled() && steps.review.outputs.review != '' && steps.review.outputs.update-comment == 'true' }}` and `continue-on-error: true`. A comment or label cleanup error must not change the scan result. Do not execute code from a fork in a write-enabled job.
