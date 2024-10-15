// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package main

import (
	"flag"
	"fmt"
	"os"

	"github.com/microsoft/go-infra/install/powershellscript"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
}

func run() error {
	h := flag.Bool("h", false, "Print this help message")

	check := flag.Bool("check", false,
		"Do not write the file, just check if any change would be made.\n"+
			"Exit code 2 if there's a text difference, 1 if there's an error, 0 if the file matches this command's payload.")

	flag.Parse()

	if *h {
		fmt.Println(
			"This command creates (by default) a PowerShell (pwsh) script named 'microsoft-go-install.ps1' in the current directory.",
			"The script contains its own documentation.")
		flag.PrintDefaults()
		return nil
	}
	if *check {
		return runCheck()
	}

	if err := os.WriteFile(powershellscript.Name, []byte(powershellscript.Content), 0o777); err != nil {
		return err
	}
	fmt.Println("Created " + powershellscript.Name)
	return nil
}

func runCheck() error {
	existing, err := os.ReadFile(powershellscript.Name)
	if err != nil {
		return err
	}
	if string(existing) != powershellscript.Content {
		fmt.Println(powershellscript.Name + " file content differs from this command's payload.")
		os.Exit(2)
	}
	fmt.Println(powershellscript.Name + " file content matches this command's payload.")
	return nil
}
