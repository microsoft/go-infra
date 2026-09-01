# github.com/microsoft/go-infra/assets

[![Go Reference](https://pkg.go.dev/badge/github.com/microsoft/go-infra/assets.svg)](https://pkg.go.dev/github.com/microsoft/go-infra/assets)

The `assets` module defines JSON contracts for artifacts from the Microsoft build of Go.
[`ToolsetBuild`](./assets.go) describes the artifacts produced by one toolset build.
[`Branch`](./branch.go) describes a release branch, its support status, and stable links to its latest artifacts.

Build and release tools use these models to exchange version, platform, URL, checksum, and signature information.

## Why this is a separate module

Some consumers of the asset format must compile with older Go releases.
For example, a `golang.org/dl`-style version wrapper can help a user upgrade from an old Go release to the current release.
Such a tool cannot import the main `go-infra` module because that module uses newer Go features and dependencies.

The `assets` module declares Go 1.18 and contains only the data model.
Code that parses versions, updates repositories, or applies release policy remains in the main `go-infra` module.
This boundary lets older tools read current asset files without importing the build and release implementation.

## Compatibility

The JSON field names are part of the contract between build producers and consumers.
Changes to the model must continue to decode existing asset files and support tools that use Go 1.18.

## Origin

The `ToolsetBuild` model was originally based on the [`golang` Docker Official Image `versions.json` model](https://github.com/docker-library/golang/blob/master/versions.json).
Later changes made the model a general source of release information for the Microsoft build of Go.
