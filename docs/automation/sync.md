# Automated branch sync

microsoft/go-infra implements branch sync infrastructure for the Microsoft build of Go repositories.
Sync is used to:

* Keep Microsoft's forks related to Go up to date with upstream repos.
* Update documentation in release branches to match the main branch.

Updating forks sometimes involves automatically resolving merge conflicts, so the infra is implemented here rather than relying on existing infra that assumes clean merges.

These files control how automated sync operates:

* [/eng/sync-config.json](/eng/sync-config.json) configures the list of branches to sync.
* [/sync/model.go](/sync/model.go) documents the configuration file format.
* [/cmd/sync/main.go](/cmd/sync/main.go) contains the sync command entrypoint.
* [/eng/pipelines/upstream-sync-pipeline.yml](/eng/pipelines/upstream-sync-pipeline.yml) is the pipeline that periodically runs sync, and it defines the schedule shared by all configurations.

Sync applies `sync:docs` to documentation PRs and `sync:upstream` to submodule update PRs, including existing PRs that it updates.
It creates missing repository labels.

## Preparing the next release branch

Scheduled sync prepares only [`msmajorversions.Next`](/msmajorversions), on the first run that
sees `release-branch.go1.N` in upstream Go and its configured mirror. No release tag is needed.
This does not publish a release, promote a version, or update downloads. `releasego sync`
continues to handle individual releases.

Only the official sync pipeline opts in with `-prepare-next-release`. An ordinary sync or a run
of the unofficial pipeline does not cut branches. The `PrepareNextRelease` entry in
[/eng/sync-config.json](/eng/sync-config.json) selects the repository; its default `{}` forks
the missing Microsoft release branch from `microsoft/main`.
To fork before an incompatible change, set a version-specific override before the cut, e.g.
`"PrepareNextRelease": {"1.28": "<full commit SHA on microsoft/main>"}`. An existing branch is
never reset. Omit the field to disable preparation.

The bot opens two PRs for human review, without enabling auto-merge:

* On `microsoft/release-branch.go1.N`, pin the `go` submodule to the latest commit shared by
  upstream's release branch and its mirror. The published PR head records that choice across
  retries. Fix any remaining patch conflicts on the PR. A manually prepared release-line commit
  is left alone; a manual move to a commit before the upstream branch point or outside the
  release branch's history stops preparation for review.
* On `microsoft/main`, start the notes for **1.(N+1)**. The 1.N notes stay on the cut branch;
  remove the main copy only when the cut branch contains identical notes.

Reruns leave open preparation PRs and existing notes alone. If publishing a PR head succeeds
but posting the PR fails, the retry verifies the retained patch against its original parent and
reuses the head. Unrelated changes on main don't invalidate it. To preview without pushing
branches or submitting PRs, run `go run ./cmd/sync -prepare-next-release -n`.

## Patch updates

When a sync entry updates a submodule managed by a `.git-go-patch` file at the target repository root, sync checks each patch against the new upstream commit.
It first applies each patch normally and retries a failed patch with `git am --3way`.

If that merge conflicts in `go.mod`, `go.sum`, or `vendor/modules.txt`, sync also handles the narrow case where a patch only inserts lines and upstream only updates existing dependency versions (and their checksums).
It keeps the patch's additions and upstream's versions without running Go commands or downloading modules.
The base lines must align unambiguously, and upstream must not add or remove lines in the conflicted file.
Edits to the same dependency by both sides, other upstream changes, and conflicts in other files still require manual resolution.

If the three-way merge succeeds, sync regenerates and stages only that patch.
Patches that apply normally remain byte-for-byte unchanged to keep the sync PR diff focused.
Regenerated patches are buffered until the entire patch set succeeds, so an unresolved conflict leaves all patch files unchanged and the sync PR can proceed for manual repair.
When Dockerfile regeneration is enabled, an unresolved patch conflict also skips regeneration with a diagnostic, so the sync PR can still be published for manual repair.
When index preservation is enabled, sync restores the index lines before applying each patch and stages the updated `index.json` alongside any regenerated patches.

For help resolving patch conflicts, see [the patch fixup section of the `git-go-patch` README](/cmd/git-go-patch#fix-up-patch-files-after-a-submodule-update).

## Dockerfile regeneration

The `RegenerateDockerfiles` option can be enabled for a submodule sync in go-images.
`SubmoduleTarget` specifies the upstream Docker repository's submodule path. When the submodule commit changes, sync regenerates `src/microsoft` Dockerfiles from the new
upstream templates and stages the generated files in the sync PR. This requires `bash`, `jq`, and
`awk` on the sync agent.

## Documentation updates

The `SyncDocs` entry in [/eng/sync-config.json](/eng/sync-config.json) copies documentation from `microsoft/go`'s `microsoft/main` branch to the current and previous stable support branches defined by [msmajorversions](/msmajorversions).
It runs on the same schedule as the `microsoft/main` submodule updates, and it runs even when the submodule is already up to date.

The sync replaces the paths listed in [`docsSyncPaths`](/sync/docs.go), including all files recursively under the listed directories, and it cleans up files that have been removed in main.
Each copied Markdown document starts with an HTML comment linking to its maintained version on `microsoft/main` warning `DO NOT EDIT`.

Documentation update PRs are independent of the submodule sync PRs.
They use their own branch naming format to avoid collision.
This allows us to sync documentation to release branches rapidly.
Release-specific submodule sync does not run documentation sync, so documentation sync failures do not interrupt releases.
The shared scheduled pipeline still reports a failure if documentation sync fails, but continues processing the other sync entries.

In a documentation sync entry, `Upstream` identifies the source repository, `Target` identifies the destination repository, and `MainBranch` names the source branch.
All three fields are required.

A documentation sync entry cannot specify `BranchMap`, `AutoSyncBranches`, or `SubmoduleTarget`.
These configure branch merges and submodule updates instead, which must be treated as an isolated sync process.
