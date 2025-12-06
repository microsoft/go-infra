# go-install.ps1

[`go-install.ps1`](powershell/go-install.ps1) is a PowerShell script that installs the [Microsoft build of Go](https://github.com/microsoft/go) toolset.
The script works with Windows PowerShell and PowerShell (`pwsh`) and can install all [supported prebuilt Microsoft build of Go toolset platforms](https://github.com/microsoft/go?tab=readme-ov-file#download-and-install).
It installs the Microsoft build of Go toolset into a directory of your choice, or defaults to a directory in the user-specific data directory.

Run `go-install.ps1 -h` to see more information about its parameters and defaults.

The script is intended for use in CI/CD pipelines or to reproduce the results of those CI/CD pipelines locally.

We recommend using tools from the `github.com/microsoft/go-infra/goinstallscript` module described in [the module README](README.md) to set up the script in your repository and make sure it stays up to date.

## Prerequisites

On non-Windows platforms, install [PowerShell (`pwsh`)](https://learn.microsoft.com/en-us/powershell/scripting/install/installing-powershell).

On Windows, either Windows PowerShell or PowerShell can be used.

> [!NOTE]
> PowerShell was formerly known as PowerShell Core.
> Now [Windows PowerShell and PowerShell](https://learn.microsoft.com/en-us/powershell/scripting/what-is-windows-powershell) are the names used by Microsoft for these products.

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
