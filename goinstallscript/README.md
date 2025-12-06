# github.com/microsoft/go-infra/goinstallscript

[![Go Reference](https://pkg.go.dev/badge/github.com/microsoft/go-infra/goinstallscript.svg)](https://pkg.go.dev/github.com/microsoft/go-infra/goinstallscript)

The `goinstallscript` module helps acquire and run the `go-install.ps1` script in a way that keeps it up to date.

For more information about using the `go-install.ps1` script itself, see [the install script README](README-InstallScript.md).

## Bootstrapping

In some cases, it's acceptable to use some arbitrary copy of Go (a "bootstrap" copy) to install another copy of Go, then only use the second copy to build your application.
We recommend this approach when possible, because it works better with Dependabot and requires minimal maintenance effort.

> [!IMPORTANT]
> It's common that a bootstrap approach is not feasible.
> For example, it may be forbidden because it's too risky to involve multiple copies of Go in a build pipeline.
> It might also be forbidden for supply chain reasons.
>
> We've included features in the `goinstallscript` command that help with this case.
> See [Maintaining a checked-in `go-install.ps1` script](CheckedInScript.md) for more information.

The rest of this document describes how to use a bootstrap copy of Go with the `goinstall` command to install the Microsoft build of Go.

The bootstrap copy of Go should be a supported, secure version of the official Go distribution or the Microsoft build of Go.
You might have a copy of `go` included in your build VM or build image: this is typically sufficient.
If not, the [Microsoft build of Go migration guide](https://github.com/microsoft/go/blob/microsoft/main/eng/doc/MigrationGuide.md) lists a variety of ways to install Go.

## Setup

On your machine, run this command inside your Go module:

```
go get -tool github.com/microsoft/go-infra/goinstallscript/cmd/goinstall@latest
```

This sets up a [tool dependency](https://go.dev/doc/modules/managing-dependencies#tools) on the `goinstall` command from this module.
Check in the changes `go` makes to your `go.mod` and `go.sum` files.

> [!TIP]
> If you want to isolate the tool's dependencies from the rest of your Go module dependencies, consider creating a separate Go module that is just for this tool.

To keep the tool up to date, make sure [dependabot](https://github.com/dependabot) or a similar Go module update tool is working.

## Usage

In your pipeline or build script, use this Go command to run the `go-install.ps1` script and install the latest version of the Microsoft build of Go:

```
go run github.com/microsoft/go-infra/goinstallscript/cmd/goinstall -- -Version Latest
```

> [!NOTE]
> You must run these commands in a directory that belongs to the Go module with the tool dependency.

See [go-install.ps1](powershell/go-install.ps1) for more information about the options available to the `go-install.ps1` script.
Pass any of those options to the `goinstall` command after the `--` argument.

To view the help text for the `goinstall` command, run:

```
go run github.com/microsoft/go-infra/goinstallscript/cmd/goinstall -h
```

### Usage in Azure Pipelines

This YAML snippet installs the Microsoft build of Go and prepends it to PATH using an Azure Pipelines Logging Command for use in subsequent steps.

```yaml
- script: |
    go run github.com/microsoft/go-infra/goinstallscript/cmd/goinstall -- -Version Latest -AzurePipelinePath
  displayName: 'Install Microsoft build of Go'
```

### Usage in GitHub Actions

This YAML snippet installs the Microsoft build of Go and prepends it to PATH using the GITHUB_PATH file for use in subsequent steps.

```yaml
- name: 'Install Microsoft build of Go'
  run: |
    go run github.com/microsoft/go-infra/goinstallscript/cmd/goinstall -- -Version Latest -GitHubActionsPath
```

# Support

Report issues and ask questions by filing an issue in the [microsoft/go](https://github.com/microsoft/go) repository.
