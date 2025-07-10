// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package main

import (
	"flag"
	"fmt"
	"os"
	"os/exec"
	"strings"

	"github.com/microsoft/go-infra/subcmd"
)

func init() {
	subcommands = append(subcommands, subcmd.Option{
		Name:    "rebase-interactive",
		Summary: "Start an interactive terminal in the submodule for rebase operations, then automatically extract patches.",
		Description: `

This command opens an interactive terminal session in the submodule directory, making it easy to
perform rebase operations without manually navigating to the submodule. The terminal prompt is
modified to indicate you're in the git-go-patch rebase mode.

When you exit the terminal (by typing 'exit'), the command automatically runs 'git go-patch extract'
to save your changes back to patch files.

This workflow combines the manual steps:
1. git go-patch rebase
2. [manual cd to submodule and perform rebase work]  
3. git go-patch extract

Into a streamlined process where steps 1 and 3 are automated, and step 2 happens in a dedicated
terminal environment.

Use case: This is particularly useful when you need to perform complex rebase operations that
would benefit from multiple commands, or when you want to open VS Code in the submodule context
(run 'code .' inside the interactive terminal).
` + repoRootSearchDescription,
		Handle: handleRebaseInteractive,
	})
}

func handleRebaseInteractive(p subcmd.ParseFunc) error {
	skipExtract := flag.Bool("skip-extract", false, "Skip automatic extraction when exiting the interactive session.")

	if err := p(); err != nil {
		return err
	}

	config, err := loadConfig()
	if err != nil {
		return err
	}
	_, goDir := config.FullProjectRoots()

	// Check that we have a valid pre-patch status file (like regular rebase command)
	since, err := readStatusFile(config.FullPrePatchStatusFilePath())
	if err != nil {
		return fmt.Errorf("no pre-patch status found - run 'git go-patch apply' first: %w", err)
	}

	fmt.Printf("Starting interactive rebase session in submodule directory: %s\n", goDir)
	fmt.Printf("Base commit for rebase: %s\n", since)
	fmt.Printf("\n%s=== GIT GO-PATCH INTERACTIVE REBASE MODE ===%s\n",
		"\033[1;32m", "\033[0m") // Green bold text with reset
	fmt.Printf("\nYou are now in a nested terminal session. The prompt shows '(git-go-patch)' to remind you.\n")
	fmt.Printf("\nSuggested workflow:\n")
	fmt.Printf("  - Run 'git rebase -i %s' to start an interactive rebase\n", since)
	fmt.Printf("  - Run 'code .' to open VS Code in the submodule context\n")
	fmt.Printf("  - Use any other git commands as needed\n")
	fmt.Printf("  - When done, type 'exit' to return to the main session\n")

	if !*skipExtract {
		fmt.Printf("\nWhen you exit, 'git go-patch extract' will run automatically to save your changes.\n")
		fmt.Printf("Use --skip-extract flag if you want to handle extraction manually.\n")
	} else {
		fmt.Printf("\nAutomatic extraction is disabled. You'll need to run 'git go-patch extract' manually.\n")
	}

	fmt.Printf("\n%s=== Press Enter to continue ===%s\n", "\033[1;33m", "\033[0m") // Yellow text
	_, _ = fmt.Scanln()                                                            // Wait for user to press Enter

	// Determine the user's shell
	shell := os.Getenv("SHELL")
	if shell == "" {
		shell = "/bin/bash" // Default fallback
	}

	// Create a modified environment with updated PS1
	env := os.Environ()

	// Find and update PS1, or add it if it doesn't exist
	ps1Updated := false
	for i, envVar := range env {
		if strings.HasPrefix(envVar, "PS1=") {
			// Extract the current PS1 value
			currentPS1 := envVar[4:] // Remove "PS1=" prefix
			// Prepend our indicator
			newPS1 := fmt.Sprintf("PS1=(git-go-patch) %s", currentPS1)
			env[i] = newPS1
			ps1Updated = true
			break
		}
	}

	// If PS1 wasn't found in environment, add a default one
	if !ps1Updated {
		env = append(env, "PS1=(git-go-patch) \\u@\\h:\\w\\$ ")
	}

	// Add an environment variable to indicate we're in git-go-patch mode
	env = append(env, "GIT_GO_PATCH_INTERACTIVE=1")

	// Start the interactive shell in the submodule directory
	cmd := exec.Command(shell)
	cmd.Dir = goDir
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	cmd.Env = env

	fmt.Printf("\nStarting shell in %s...\n", goDir)
	fmt.Printf("Current working directory will be: %s\n\n", goDir)

	err = cmd.Run()
	if err != nil {
		fmt.Printf("\nShell exited with error: %v\n", err)
		if !*skipExtract {
			fmt.Printf("Continuing with extract process...\n")
		}
	} else {
		fmt.Printf("\nShell session completed successfully.\n")
	}

	// Skip extract if user requested it
	if *skipExtract {
		fmt.Printf("\nSkipping automatic extraction as requested.\n")
		fmt.Printf("Remember to run 'git go-patch extract' manually when you're ready.\n")
		return nil
	}

	// Now automatically run extract
	fmt.Printf("\nAutomatically running 'git go-patch extract' to save your changes...\n")

	// We need to run the extract command from the original working directory
	// since extract expects to be run from the repo root area
	originalWd, err := os.Getwd()
	if err != nil {
		return fmt.Errorf("failed to get current working directory: %w", err)
	}

	// Create a new extract command and run it
	extractCmd := exec.Command("git", "go-patch", "extract")
	extractCmd.Dir = originalWd
	extractCmd.Stdin = os.Stdin
	extractCmd.Stdout = os.Stdout
	extractCmd.Stderr = os.Stderr

	if err := extractCmd.Run(); err != nil {
		return fmt.Errorf("failed to run 'git go-patch extract': %w", err)
	}

	fmt.Printf("\n%s=== Interactive rebase session completed successfully! ===%s\n",
		"\033[1;32m", "\033[0m") // Green bold text
	fmt.Printf("Your changes have been extracted to patch files.\n")

	return nil
}
