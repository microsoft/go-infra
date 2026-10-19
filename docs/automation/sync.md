# Automated branch sync

microsoft/go-infra implements branch sync infrastructure for the Microsoft build of Go repositories.
Sync is used to keep Microsoft's repos up to date with upstream repos, and to update dev branches with the latest changes from their upstream branches.

This sometimes involves automatically resolving merge conflicts, so the infra is implemented here rather than relying on existing infra that assumes clean merges.

## Documentation updates

The `SyncDocs` entry in [/eng/sync-config.json](/eng/sync-config.json) copies documentation from `microsoft/go`'s `microsoft/main` branch to the current and previous stable support branches defined by [msmajorversions](/msmajorversions).
It runs on the same schedule as the `microsoft/main` submodule updates, even when the submodule is already up to date.

The sync replaces `README.md`, `SUPPORT.md`, `SECURITY.md`, and all files recursively under `eng/doc/`, including adding new files and removing files deleted on main.
Each copied Markdown document starts with an HTML comment linking to its maintained version on `microsoft/main`, naming the destination branch, and warning `DO NOT EDIT`.
Other files, such as JSON metadata and images, are copied unchanged to preserve their formats.
Make documentation changes on `microsoft/main`; edits to copied documents on support branches are overwritten on the next sync.

Documentation-only PRs use `dev/auto-sync-docs/<target branch>` so they do not interfere with submodule/release sync PRs.
They do not merge main into support branches or change code, patches, version files, or submodule pointers.
The entry requires `Upstream`, `Target`, and `MainBranch`; `BranchMap`, `AutoSyncBranches`, and `SubmoduleTarget` must be omitted.

## Patch updates

When a sync entry updates a submodule managed by a `.git-go-patch` file at the target repository root, sync checks each patch against the new upstream commit.
It first applies each patch normally and retries a failed patch with `git am --3way`.

If the three-way merge succeeds, sync regenerates and stages only that patch.
Patches that apply normally remain byte-for-byte unchanged to keep the sync PR diff focused.
Regenerated patches are buffered until the entire patch set succeeds, so an unresolved conflict leaves all patch files unchanged and the sync PR can proceed for manual repair.
When index preservation is enabled, sync restores the index lines before applying each patch and stages the updated `index.json` alongside any regenerated patches.

For help resolving patch conflicts, see [the patch fixup section of the `git-go-patch` README](/cmd/git-go-patch#fix-up-patch-files-after-a-submodule-update).

These files control how automated sync operates:

* [/eng/sync-config.json](/eng/sync-config.json) configures the list of branches to sync.
* [/sync/model.go](/sync/model.go) documents the configuration file format.
* [/cmd/sync/main.go](/cmd/sync/main.go) contains the sync command entrypoint.
* [/eng/pipelines/upstream-sync-pipeline.yml](/eng/pipelines/upstream-sync-pipeline.yml) is the pipeline that periodically runs sync, and it defines the schedule shared by all configurations.
