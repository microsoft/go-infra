# Automated branch sync

microsoft/go-infra implements branch sync infrastructure for the Microsoft build of Go repositories.
Sync is used to keep Microsoft's repos up to date with upstream repos, and to update dev branches with the latest changes from their upstream branches.

This sometimes involves automatically resolving merge conflicts, so the infra is implemented here rather than relying on existing infra that assumes clean merges.

## Patch updates

When a sync entry updates a submodule managed by a `.git-go-patch` file at the target repository
root, sync checks each patch against the new upstream commit. It first applies each patch normally
and retries a failed patch with `git am --3way`.

If the three-way merge succeeds, sync regenerates and stages only that patch. Patches that apply
normally remain byte-for-byte unchanged to keep the sync PR diff focused. Regenerated patches are
buffered until the entire patch set succeeds, so an unresolved conflict leaves all patch files
unchanged and the sync PR can proceed for manual repair.

For help resolving patch conflicts, see [the patch fixup section of the `git-go-patch` README](/cmd/git-go-patch#fix-up-patch-files-after-a-submodule-update).

These files control how automated sync operates:

* [/eng/sync-config.json](/eng/sync-config.json) configures the list of branches to sync.
* [/sync/model.go](/sync/model.go) documents the configuration file format.
* [/cmd/sync/main.go](/cmd/sync/main.go) contains the sync command entrypoint.
* [/eng/pipelines/upstream-sync-pipeline.yml](/eng/pipelines/upstream-sync-pipeline.yml) is the pipeline that periodically runs sync, and it defines the schedule shared by all configurations.
