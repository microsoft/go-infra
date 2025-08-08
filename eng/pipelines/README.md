# Pipelines

This directory contains Azure DevOps (AzDO) YAML pipelines for CI and utilities.

* [The dnceng-public Go folder](https://dev.azure.com/dnceng-public/public/_build?definitionScope=%5CMicrosoft%5Cgo) contains public Go pipelines used by PR validation.
* [The internal dnceng Go folder](https://dev.azure.com/dnceng/internal/_build?definitionScope=%5CMicrosoft%5Cgo) contains internal pipelines, like CI and release pipelines.

Each pipeline yml file contains links to its pipeline or pipelines.

See [the release process design docs](/docs/release-process/design.md) for more information about the sequence of the `release-*` pipelines.
