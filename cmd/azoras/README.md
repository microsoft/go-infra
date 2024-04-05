# azoras

azoras is a tool that helps work with the ORAS CLI and Azure ACR.
The subcommands implement common workflows for image annotations and maintenance.

## Installation

```shell
go install github.com/go-infra/cmd/azoras@latest
```

Optionally, azoras can install the ORAS CLI for you running:

```shell
azoras install
```

## Authentication

azoras doesn't handle authentication, you need to login to your Azure ACR registry with the ORAS CLI.
See https://oras.land/docs/how_to_guides/authentication for more information.

azoras provides a helper subcommand to login to login the ORAS CLI to your Azure ACR registry using the Azure CLI.
You first need to login to the Azure CLI with `az login` and then run:

```shell
azoras login
```

## Subcommands

### `azoras deprecate`

Deprecate an image in an Azure ACR registry.

```shell
azoras deprecate myregistry.azurecr.io/myimage:sha256:foo
```

This subcommand can also deprecated multiple images at once using a file with a line-separated list of images.

```shell
azoras deprecate images.txt -bulk
```
