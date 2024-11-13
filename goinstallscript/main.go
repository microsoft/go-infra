// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package main

import (
	"flag"
	"fmt"
	"os"
	"strings"

	"github.com/microsoft/go-infra/goinstallscript/powershell"
)

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

	if *check {
		return runCheck(*name)
	}

	if err := os.WriteFile(*name, []byte(powershell.Content), 0o777); err != nil {
		return err
	}
	fmt.Println("Created " + *name)
	return nil
}

func runCheck(name string) error {
	existing, err := os.ReadFile(name)
	if err != nil {
		return err
	}
	if string(existing) == powershell.Content {
		fmt.Println("Check ok: " + name + " file matches expected content.")
		return nil
	}
	// Accept CRLF as well. The user might be using autocrlf on Windows.
	if strings.ReplaceAll(string(existing), "\r\n", "\n") == powershell.Content {
		fmt.Println("Check ok: " + name + " file contains CRLF line endings but otherwise matches expected content.")
		return nil
	}
	fmt.Println("Check failed: " + name + " file differs from expected content.")
	os.Exit(2)
	panic("unreachable: command should have exited with status 2")
}
