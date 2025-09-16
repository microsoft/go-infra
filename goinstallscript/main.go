// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package main

import (
	"errors"
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
	// Get absolute path for clarity in messages.
	absWd, err := os.Getwd()
	if err != nil {
		return err
	}

	existing, err := os.ReadFile(name)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("file %#q does not exist in %#q", name, absWd)
		}
		return err
	}
	if string(existing) == powershell.Content {
		fmt.Printf("Check ok: file %#q matches expected content.\n", name)
		return nil
	}
	// Accept CRLF as well. The user might be using autocrlf on Windows.
	if strings.ReplaceAll(string(existing), "\r\n", "\n") == powershell.Content {
		fmt.Printf("Check ok: file %#q contains CRLF line endings but otherwise matches expected content.\n", name)
		return nil
	}
	fmt.Printf("Check failed: file %#q has unexpected content.\n", name)

	fmt.Printf("To update it, go to %#q and run:\n", absWd)
	var nameArg string
	if name != powershell.Name {
		nameArg = " -name " + name
	}
	fmt.Printf("  go run github.com/microsoft/go-infra/goinstallscript%v\n", nameArg)

	os.Exit(2)
	panic("unreachable: command should have exited with status 2")
}
