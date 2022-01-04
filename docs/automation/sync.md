# Automated branch sync

microsoft/go-infra implements branch sync infrastructure for the Microsoft Go repositories. Sync is used to keep Microsoft's repos up to date with upstream repos, and to update dev branches with the latest changes from their upstream branches. This sometimes involves automatically resolving merge conflicts, so the infra is implemented here rather than relying on existing infra that assumes clean merges.

* [/eng/sync-config.json](/eng/sync-config.json) configures the list of branches to sync.
* [/cmd/sync/model.go](/cmd/sync/model.go) documents the configuration file format.
* [/cmd/sync/sync.go](/cmd/sync/sync.go) contains the sync command entrypoint.
* [/eng/pipelines/sync-pipeline.yml](/eng/pipelines/sync-pipeline.yml) is the pipeline that periodically runs sync, and it defines the schedule shared by all configurations.
