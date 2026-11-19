# upstreamreview

`upstreamreview` checks a GitHub.com pull request for changes to Go importers in an upstream submodule. It compares the revisions pinned in the PR base and proposed merge using the same rules as [`importdiff`](../importdiff/). The Markdown summary links to each match and gives the equivalent `importdiff` command.

Run it from a checkout of the PR test merge with `fetch-depth: 2` and a `GH_TOKEN` with read access to pull requests in the target repository. To use it as a Go tool, add `github.com/microsoft/go-infra/cmd/upstreamreview` to the consuming module's `tool` directives and pin the module version. For example, in a `pull_request` workflow with `PR` set to the event's PR number:

```sh
go tool upstreamreview -repo "$GITHUB_REPOSITORY" -pr "$PR" -merge "$GITHUB_SHA" \
  -upstream golang/go -submodule go -label upstream-crypto-reviewed \
  -src src -import crypto/internal/fips140 -exclude src/crypto/internal/fips140 \
  >> "$GITHUB_STEP_SUMMARY"
```

`-repo` is the repository that owns the PR and label; `-upstream` is the GitHub `owner/name` used to fetch source and build diff links. Set it in the workflow. `-submodule` is a path relative to the target repository root. Supply at least one `-src` and one `-import`; both are repeatable, as is the optional `-exclude`. Source and excluded directories are relative to the upstream repository root.

The check passes if the scan is clean or the live PR has the configured label. The label applies to the whole PR, including later commits. Enable the `labeled` and `unlabeled` PR activities so adding or removing it reruns the check. A failed scan or a checkout that no longer matches the live PR fails even if the label is present.
