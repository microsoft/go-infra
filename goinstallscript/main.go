// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package main

import (
	"bytes"
	"flag"
	"fmt"
	"os"
	"os/exec"

	"github.com/microsoft/go-infra/goinstallscript/powershell"
)

// scriptEmitPackage is a Go package that emits the PowerShell script content. The goinstallscript
// command runs this package as indirection rather than directly using powershell.Content. This
// allows a single build of goinstallscript to be used to install multiple versions of the script.
const scriptEmitPackage = "github.com/microsoft/go-infra/goinstallscript/powershell/print"

func main() {
	if err := run(); err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
}

func run() error {
	h := flag.Bool("h", false, "Print this help message")

	name := flag.String("name", powershell.Name, "Name of the script file to create.")
	check := flag.Bool("check", false,
		"Do not write the file, just check if any change would be made.\n"+
			"Exit code 2 if there's a text difference, 1 if there's an error, 0 if the file matches this command's payload.")

	flag.Parse()

	if *h {
		fmt.Println(
			"This command creates (by default) a PowerShell (pwsh) script named 'go-install.ps1' in the current directory.",
			"The script contains its own documentation.")
		flag.PrintDefaults()
		return nil
	}

	content, err := getScriptContent()
	if err != nil {
		return err
	}

	if *check {
		return runCheck(*name, content)
	}

	if err := os.WriteFile(*name, []byte(content), 0o777); err != nil {
		return err
	}
	fmt.Println("Created " + *name)
	return nil
}

func getScriptContent() ([]byte, error) {
	fmt.Println("Running " + scriptEmitPackage + " in the context of the current directory (current Go module) to retrieve PowerShell script content...")
	cmd := exec.Command("go", "run", scriptEmitPackage)
	content, err := cmd.CombinedOutput()
	if err != nil {
		return nil, fmt.Errorf("error running %v: %v; output:\n---\n%s\n---", cmd, err, content)
	}
	return content, err
}

func runCheck(name string, content []byte) error {
	fmt.Println("Checking " + name + " file content to see if it's up to date...")
	existing, err := os.ReadFile(name)
	if err != nil {
		return err
	}
	if bytes.Equal(existing, content) {
		fmt.Println("Check ok: " + name + " file matches expected content.")
		return nil
	}
	// Accept CRLF as well. The user might be using autocrlf on Windows.
	if bytes.Equal(bytes.ReplaceAll(existing, []byte("\r\n"), []byte("\n")), content) {
		fmt.Println("Check ok: " + name + " file contains CRLF line endings but otherwise matches expected content.")
		return nil
	}
	fmt.Println("Check failed: " + name + " file differs from expected content.")
	os.Exit(2)
	panic("unreachable: command should have exited with status 2")
}
