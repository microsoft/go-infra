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

azoras provides a helper subcommand to login the ORAS CLI to your Azure ACR registry using the Azure CLI.

1. Log into the Azure CLI:
    ```shell
    az login
    ```
2. Use the utility command to log into your ACR:
    ```shell
    azoras login <acr-name>
    ```
    An ACR name is `golangimages`, *not* its login server URL.

It is also possible to login to the ORAS CLI manually using the `oras login` command.
See the [ORAS CLI Authentication](https://oras.land/docs/how_to_guides/authentication/) documentation for more information.

## Subcommands

### `azoras deprecate`

Deprecate an image in an Azure ACR registry.

```shell
azoras deprecate myregistry.azurecr.io/myimage:sha256:foo
```

This subcommand can also deprecated multiple images at once using a file with a line-separated list of images.

```shell
azoras deprecate -bulk images.txt
```
