# Maintaining a checked in `go-install.ps1` script

If you need to install the Microsoft build of Go toolset on a machine that has PowerShell installed without installing Go or any other dependencies, you may need to check the `go-install.ps1` script into your repository.
The `goinstallscript` command is designed to help with this process.

For more information about using the `go-install.ps1` script itself, see [the install script README](README-InstallScript.md).

## Set up `goinstallscript` in your repository

Open a terminal in the directory inside your Go module where you want to store the `go-install.ps1` script.
Then, run these commands to get the latest version of the module and run `goinstallscript`:

```
go get -tool github.com/microsoft/go-infra/goinstallscript@latest
go run github.com/microsoft/go-infra/goinstallscript
```

This creates `go-install.ps1` in the current directory.

To use `go-install.ps1` in your build pipeline or build scripts, see [the install script README](README-InstallScript.md).

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
