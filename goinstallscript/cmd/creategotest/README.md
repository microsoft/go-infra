# creategotest

This command creates a Go test file that checks whether the `go-install.ps1` script is up to date.

See [the module README](../../README.md#adding-a-go-test-to-check-for-updates) for details.

The generated test file is written to `goinstallscript/goinstallscript_test.go` relative to the current directory.
It's generated in a subdirectory to avoid a name conflict with the `package` declaration of the current directory, and to clearly isolate it.
