# go-install.ps1

[`go-install.ps1`](powershell/go-install.ps1) is a PowerShell script that installs the [Microsoft build of Go](https://github.com/microsoft/go) toolset.
The script works with Windows PowerShell and PowerShell (`pwsh`) and can install all [supported prebuilt Microsoft build of Go toolset platforms](https://github.com/microsoft/go?tab=readme-ov-file#download-and-install).
It installs the Microsoft build of Go toolset into a directory of your choice, or defaults to a directory in the user-specific data directory.

Run `go-install.ps1 -h` to see more information about its parameters and defaults.

The script is intended for use in CI/CD pipelines or to reproduce the results of those CI/CD pipelines locally.

Use [`github.com/microsoft/go-infra/goinstallscript`](#githubcommicrosoftgo-infragoinstallscript) to ensure `go-install.ps1` is up to date.

## Prerequisites

On non-Windows platforms, install [PowerShell (`pwsh`)](https://learn.microsoft.com/en-us/powershell/scripting/install/installing-powershell).

On Windows, either Windows PowerShell or PowerShell can be used.

> [!NOTE]
> PowerShell was formerly known as PowerShell Core.
> Now [Windows PowerShell and PowerShell](https://learn.microsoft.com/en-us/powershell/scripting/what-is-windows-powershell) are the names used by Microsoft for these products.

## Installing the script

We recommend following the instructions for [`github.com/microsoft/go-infra/goinstallscript`](#githubcommicrosoftgo-infragoinstallscript) to set up the script in your repository.
If you have specific requirements for the location or name of the script, it can be renamed or placed anywhere without affecting its functionality.

## Usage

For any platform: run the script using the `pwsh` command:

```bash
pwsh ./go-install.ps1
```

If you're using Azure Pipelines, pass `-AzurePipelinePath` to make `go` commands work in future steps.

Pass `-h` to show help.

> [!NOTE]
> If you use a PowerShell terminal, you can choose to run the script directly:
>
> ```
> .\go-install.ps1
> ```
>
> Running the script directly has a benefit: it allows the script to change the terminal's `PATH` so the installed Go binary is then available in the current PowerShell session as `go`.
>
> Note that in typical CI/CD pipelines, each step is run in a fresh process and the `PATH` change will not be preserved in future steps.
> For that, use `-AzurePipelinePath` or preserve the `PATH` change in another way.

# github.com/microsoft/go-infra/goinstallscript

[![Go Reference](https://pkg.go.dev/badge/github.com/microsoft/go-infra/goinstallscript.svg)](https://pkg.go.dev/github.com/microsoft/go-infra/goinstallscript)

The `goinstallscript` command helps install `go-install.ps1` and keep it up to date.

## Set up `goinstallscript` in your repository

Open a terminal in the directory inside your Go module where you want to store the `go-install.ps1` script.
Then, run these commands to get the latest version of the module and run `goinstallscript`:

```
go get -tool github.com/microsoft/go-infra/goinstallscript@latest
go run github.com/microsoft/go-infra/goinstallscript
```

This creates `go-install.ps1` in the current directory.

Run `go run github.com/microsoft/go-infra/goinstallscript -h` for more information about the parameters and defaults.

> [!NOTE]
> We recommend against using `go install` to install the `goinstallscript` command.
> The PowerShell script's content is embedded in the binary, so running an old build of `goinstallscript` may create a file with an unexpected version of the script.
>
> By using `go run`, you ensure the script matches the expected version specified by your `go.mod` file.

## Updating the script using `goinstallscript`

To update the script, run the two `go` commands again in the directory where the script is stored:

```
go get -tool github.com/microsoft/go-infra/goinstallscript@latest
go run github.com/microsoft/go-infra/goinstallscript
```

> [!NOTE]
> There is no need to run the update command every time you want a new version of the Microsoft build of Go toolset.
> Updates to the script are rare, and only occur when the lookup or download processes themselves change or a bug is found in the script's logic.

## Set up a CI step that checks for updates

First, make sure [dependabot](https://github.com/dependabot) or a similar Go module update tool is working.
It will submit PRs that update the `github.com/microsoft/go-infra/goinstallscript` dependency when a new version is released.

Unfortunately, the `go-install.ps1` script isn't integrated directly with dependabot, so it's necessary to add a CI test case that alerts a developer when an update to the script itself is necessary.

Two ways to add the test case are [adding a CI step](#adding-a-ci-step-to-check-for-updates) or [adding a Go test](#adding-a-go-test-to-check-for-updates).

> [!NOTE]
> We maintain `github.com/microsoft/go-infra/goinstallscript` as an independent module from the rest of `github.com/microsoft/go-infra` to minimize the number of updates and keep your maintenance burden low.

### Adding a CI step to check for updates

Add a CI step that runs the following command in the directory where the script is stored:

```
go run github.com/microsoft/go-infra/goinstallscript -check
```

`goinstallscript -check` exits with code 0 (success) if the script is up to date, and code 2 (failure) if the script is out of date.
CI reads the exit code, so this step is all that's necessary to perform the check.

The error message includes instructions for updating the script, which a developer needs to follow when an error occurs.

### Adding a Go test to check for updates

In the directory where your `go-install.ps1` script is stored, run this command to generate a Go test file `goinstallscript/goinstallscript_test.go` that checks whether the script is up to date:

```
go run github.com/microsoft/go-infra/goinstallscript/cmd/creategotest
```

The generated test then runs during `go test ./...`.
If your CI already runs tests, this approach means no adjustment to your CI steps is necessary to run this check.

The test failure message includes instructions for updating the script, which a developer needs to follow when an error occurs.

> [!NOTE]
> If you want to create the file yourself or integrate it into an existing test file instead, you can use the generated file's template as a reference: [`goinstallscript_test.go`](./cmd/creategotest/_template/goinstallscript_test.go).

# Support

Report issues and ask questions by filing an issue in the [microsoft/go](https://github.com/microsoft/go) repository.
