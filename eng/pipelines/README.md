# Pipelines

This directory contains Azure DevOps (AzDO) YAML pipelines for CI and utilities.

* [The dnceng-public Go folder](https://dev.azure.com/dnceng-public/public/_build?definitionScope=%5CMicrosoft%5Cgo) contains public Go pipelines used by PR validation.
* [The internal dnceng Go folder](https://dev.azure.com/dnceng/internal/_build?definitionScope=%5CMicrosoft%5Cgo) contains internal pipelines, like CI and release pipelines.

Each pipeline yml file contains links to its pipeline or pipelines.

`release-ui-integration-test-pipeline.yml` is a manually triggered, no-op internal pipeline reserved
for a future release UI queueing integration. The current pipeline `1023` integration is read-only
and does not use it. The no-op pipeline accepts the same two template parameters as direct
go-images pipeline `1023`, but does not
import variable groups, use secrets, check out source, access external services, or publish
anything. Its Azure DevOps pipeline definition must be created after the YAML is merged, then the
assigned definition ID should be added to the file comment and to the release UI's test allowlist.

See [the release process design docs](/docs/release-process/design.md) for more information about the sequence of the `release-*` pipelines.

For information about the style of these pipelines, see [the AzDO pipeline yml style guide](/docs/pipeline-yml-style.md).
