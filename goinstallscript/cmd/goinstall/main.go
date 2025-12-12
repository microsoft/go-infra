// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package main

import (
	"cmp"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"strings"

	"github.com/microsoft/go-infra/goinstallscript/powershell"
)

const description = `
This command places the go-install.ps1 script in a temporary file then runs it
using PowerShell.

Example:

  go run github.com/microsoft/go-infra/goinstallscript/cmd/goinstall -- -Version Latest

To pass parameters to go-install.ps1, pass '--' then the list of arguments that
should be passed directly to the go-install.ps1 script.
`

func main() {
	if err := run(); err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
}

func run() error {
	pwshPath := flag.String("pwsh", "", "Path to PowerShell executable to use to run the script rather than 'pwsh' or 'powershell' from PATH")
	dryRun := flag.Bool("n", false, "Dry run mode: print the command that would be run and exit")
	scriptHelp := flag.Bool("help-script", false, "Show help header of the underlying go-install.ps1 script and exit")

	flag.Usage = func() {
		fmt.Fprintf(flag.CommandLine.Output(), "Usage of %s:\n", "goinstall")
		flag.PrintDefaults()
		fmt.Fprintf(flag.CommandLine.Output(), "%s\n", strings.TrimSpace(description))
	}
	flag.Parse()

	if *scriptHelp {
		_, comment, _, ok := cutTwice(powershell.Content, "<#", "#>")
		if !ok {
			return errors.New("failed to extract help comment from go-install.ps1")
		}
		fmt.Printf("%s\n", comment)
		return nil
	}

	if *pwshPath == "" {
		var err error
		*pwshPath, err = exec.LookPath("pwsh")
		if err != nil {
			if errors.Is(err, exec.ErrNotFound) {
				*pwshPath, err = exec.LookPath("powershell")
				if err != nil {
					return fmt.Errorf("neither 'pwsh' nor 'powershell' found in PATH")
				}
			}
		}
	}

	scriptPath, err := createScriptTemp()
	if err != nil {
		return fmt.Errorf("failed to create temporary file to store go-install.ps1: %w", err)
	}
	defer os.Remove(scriptPath)

	cmd := exec.Command(*pwshPath, "-NoProfile", "-File", scriptPath)
	cmd.Args = append(cmd.Args, flag.Args()...)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr

	if *dryRun {
		fmt.Printf("Dry run, stopping. Would have executed: %v\n", cmd.Args)
		return nil
	}

	if err := cmd.Run(); err != nil {
		return fmt.Errorf("failed to run PowerShell script: %w", err)
	}

	return nil
}

func createScriptTemp() (string, error) {
	f, err := os.CreateTemp("", "go-install-*.ps1")
	if err != nil {
		return "", fmt.Errorf("failed to create temporary file to store go-install.ps1: %w", err)
	}
	_, err = f.WriteString(powershell.Content)
	closeErr := f.Close()
	if err := cmp.Or(err, closeErr); err != nil {
		os.Remove(f.Name())
		return "", err
	}
	return f.Name(), nil
}

// cutTwice calls strings.Cut twice to split s into three strings. If either separator isn't found
// in s, returns s, "", "", false.
func cutTwice(s, sep1, sep2 string) (before, between, after string, found bool) {
	if before1, after1, found := strings.Cut(s, sep1); found {
		if between, after2, found := strings.Cut(after1, sep2); found {
			return before1, between, after2, true
		}
	}
	return s, "", "", false
}
