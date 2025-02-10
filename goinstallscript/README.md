# go-install.ps1

[`go-install.ps1`](powershell/go-install.ps1) is a PowerShell script that installs the [Microsoft build of Go](https://github.com/microsoft/go) toolset.
The script works with Windows PowerShell and PowerShell (`pwsh`) and can install all [supported prebuilt Microsoft build of Go toolset platforms](https://github.com/microsoft/go?tab=readme-ov-file#download-and-install).
It installs the Microsoft build of Go toolset into a directory of your choice, or defaults to a directory in the user-specific data directory.

See `go-install.ps1 -h` for more information about the parameters and defaults.

The script is intended for use in CI/CD pipelines or to reproduce the results of those CI/CD pipelines locally.

There is a utility command [`goinstallscript`, documented later in this readme](#githubcommicrosoftgo-infragoinstallscript), that helps install `go-install.ps1` and keep it up to date.

## Prerequisites

On non-Windows platforms, install [PowerShell (`pwsh`)](https://learn.microsoft.com/en-us/powershell/scripting/install/installing-powershell).

On Windows, either Windows PowerShell or PowerShell can be used.

> [!NOTE]
> PowerShell was formerly known as PowerShell Core.
> Now [Windows PowerShell and PowerShell](https://learn.microsoft.com/en-us/powershell/scripting/what-is-windows-powershell) are the names used by Microsoft for these products.

## Usage

Run the script using the `pwsh` command:

```bash
pwsh ./go-install.ps1
```

If you use a PowerShell terminal, you can also choose to run the script directly:

```
.\go-install.ps1
```

Running the script directly allows the script to change the terminal's `PATH` so the installed Go binary is then available in the current session as `go`.

Note that in typical CI/CD pipelines, each step is run in a fresh process and the `PATH` change will not be preserved.
If you're using Azure Pipelines, see the help message for `-AzurePipelinePath`.

Pass `-h` to show help.

## Where to put the script

The script can be placed in the root of your repository or in a subdirectory.
It can be run from a different directory with no effect on the results.
The script can be renamed and will still function properly.

To copy the script and set up a mechanism to keep it up to date, use the `goinstallscript` command.

# github.com/microsoft/go-infra/goinstallscript

The `goinstallscript` command helps install `go-install.ps1` and keep it up to date.

### Set up `goinstallscript`

In your Go module, run:

```
go get github.com/microsoft/go-infra/goinstallscript@latest
```

Then, in the directory of your choice inside your module, run:

```
go run github.com/microsoft/go-infra/goinstallscript
```

Pass `-h` for more information about the parameters and defaults.

> [!NOTE]
> It is not recommended to use `go install` to install the `goinstallscript` command.
> The PowerShell script's content is embedded in the binary, so running an old build of `goinstallscript` may create a file with an unexpected version of the script.
>
> By using `go run`, you ensure the script always matches the expected version in the `go.mod` dependency.

One more step is needed to prevent `go mod tidy` from removing the new dependency from your `go.mod` file.
Add a `tools/tools.go` file to your module with the following content:

```go
//go:build tools

package tools

import (
	_ "github.com/microsoft/go-infra/goinstallscript/powershell"
)
```

> [!NOTE]
> This is a well-known workaround to pin the version of a tool in a Go module.
> See the [Go wiki](https://go.dev/wiki/Modules#how-can-i-track-tool-dependencies-for-a-module) for more information.
>
> If you already have a file that serves this purpose for other tools, you can add the import to that file instead.

### Updating the script using `goinstallscript`

> [!NOTE]
> It isn't necessary to update the script to get new builds of the Microsoft build of Go toolset.
> Updates to the script are rare, and only occur when the lookup or download processes themselves change.

To update the script, run the `go get` and `go run` commands again in the directory where the script is stored.

### Set up a CI test to ensure the script is up to date

First, make sure dependabot is working.
It will submit PRs that update the microsoft/go-infra dependency automatically.

The script isn't integrated directly with dependabot, so it's necessary to add a test case that alerts a developer when a manual update is necessary.
This is done by adding a CI step that runs the following command in the directory where the script is stored:

```
go run github.com/microsoft/go-infra/goinstallscript -check
```

The CI step will fail if the script is not up to date because the command returns a nonzero exit code.

### Reacting to a `-check` failure

If the CI step fails, the script is out of date.
Check out the dependabot branch and use the `go run` command again to overwrite the script content with the updated version.
