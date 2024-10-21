// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

// Print writes the embedded PowerShell script to stdout and nothing else. This provides a way for
// github.com/microsoft/go-infra/goinstallscript to get the script content from a dependency stored
// in a go.mod file without any more than ordinary Go commands, and without tying the
// goinstallscript command itself to a specific version of go-install.ps1.
package main

import (
	"fmt"

	"github.com/microsoft/go-infra/goinstallscript/powershell"
)

func main() {
	fmt.Printf("%s\n", []byte(powershell.Content))
}
