# Automated branch sync

microsoft/go-infra implements branch sync infrastructure for the Microsoft Go repositories.
Sync is used to keep Microsoft's repos up to date with upstream repos, and to update dev branches with the latest changes from their upstream branches.

This sometimes involves automatically resolving merge conflicts, so the infra is implemented here rather than relying on existing infra that assumes clean merges.

For help resolving patch conflicts, see [the patch fixup section of the `git-go-patch` README](/cmd/git-go-patch#fix-up-patch-files-after-a-submodule-update).

These files control how automated sync operates:

* [/eng/sync-config.json](/eng/sync-config.json) configures the list of branches to sync.
* [/sync/model.go](/sync/model.go) documents the configuration file format.
* [/cmd/sync/main.go](/cmd/sync/main.go) contains the sync command entrypoint.
* [/eng/pipelines/upstream-sync-pipeline.yml](/eng/pipelines/upstream-sync-pipeline.yml) is the pipeline that periodically runs sync, and it defines the schedule shared by all configurations.
