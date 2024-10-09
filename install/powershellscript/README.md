# microsoft-go-install.ps1

This directory contains `microsoft-go-install.ps1`, a PowerShell script that installs the [Microsoft Go](https://github.com/microsoft/go) toolset.
It works with Windows PowerShell and PowerShell (`pwsh`) and can install all [supported prebuilt Microsoft Go toolset platforms](https://github.com/microsoft/go?tab=readme-ov-file#download-and-install).

It installs the Microsoft Go toolset into a directory of your choice, or defaults to a directory in the user-specific data directory.
See `microsoft-go-install.ps1 -h` for more information about the parameters and defaults.

The script is intended for use in CI/CD pipelines or to reproduce the results of those CI/CD pipelines locally.

## Prerequisites

On non-Windows platforms, install [PowerShell (`pwsh`)](https://learn.microsoft.com/en-us/powershell/scripting/install/installing-powershell).

On Windows, either Windows PowerShell or PowerShell can be used.

> [!NOTE]
> PowerShell was formerly known as PowerShell Core.
> Now [Windows PowerShell and PowerShell](https://learn.microsoft.com/en-us/powershell/scripting/what-is-windows-powershell) are the names used by Microsoft for these products.

## Usage

Run the script using the `pwsh` command:

```bash
pwsh ./microsoft-go-install.ps1
```

If you use a PowerShell terminal, you can also choose to run the script directly:

```
.\microsoft-go-install.ps1
```

Running the script directly allows the script to change the terminal's `PATH` so the installed Go binary is then available in the current session as `go`.

Note that in typical CI/CD pipelines, each step is run in a fresh process and the `PATH` change will not be preserved.
If you're using Azure Pipelines, see the help message for `-AzurePipelinePath`.

Pass `-h` to show help.

## Where to put the script

The script can be placed in the root of your repository or in a subdirectory.
It can be run from a different directory with no effect on the results.

To copy the script and keep it up to date in a reproducible way, use the `microsoftgoinstallscript` command.

### Set up `microsoftgoinstallscript`

In your Go module, run:

```
go get github.com/microsoft/go-infra/install/powershellscript/cmd/microsoftgoinstallscript@latest
go install github.com/microsoft/go-infra/install/powershellscript/cmd/microsoftgoinstallscript
```

If you don't already use `github.com/microsoft/go-infra` in your module, add a `tools/tools.go` file to your module to pin the version of `microsoftgoinstallscript` and prevent `go mod tidy` from removing it:

```go
//go:build tools

package tools

import (
    _ "github.com/microsoft/go-infra/install/powershellscript"
)
```

> [!NOTE]
> This is a well-known workaround to pin the version of a tool in a Go module.
> See the [Go wiki](https://go.dev/wiki/Modules#how-can-i-track-tool-dependencies-for-a-module) for more information.

Then, in the directory of your choice inside your module, run:

```
microsoftgoinstallscript
```

### Updating the script using `microsoftgoinstallscript`

To update the script, run this in the directory where the script is stored:

```
go get github.com/microsoft/go-infra/install/powershellscript/cmd/microsoftgoinstallscript@latest
go mod tidy
go install github.com/microsoft/go-infra/install/powershellscript/cmd/microsoftgoinstallscript
microsoftgoinstallscript
```

### Set up a CI test to ensure the script is up to date

First, make sure dependabot is working.
It will submit PRs that update the microsoft/go-infra dependency automatically.

Then, add a CI test that runs the following commands in the directory where the script is stored:

```
go install github.com/microsoft/go-infra/install/powershellscript/cmd/microsoftgoinstallscript
microsoftgoinstallscript -check
```

This test will fail if the script is not up to date, and you can update the branch using these commands in the script directory:

```
go install github.com/microsoft/go-infra/install/powershellscript/cmd/microsoftgoinstallscript
microsoftgoinstallscript
```

> [!NOTE]
> It isn't necessary to update the script to get new builds of the Microsoft Go toolset.
> Updates to the script are only necessary if the script itself has changed, and this is not expected to happen often once the feature set has stabilized.
