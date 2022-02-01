// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package main

import (
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

const description = `
git-go-patch is a tool that helps work with the submodule and Git patch files used by the Microsoft
Go repository. The subcommands implement common workflows for patch creation and maintenance.
`

const repoRootSearchDescription = `
This command searches the current working directory and any ancestor for "go" and "patches"
directories. If both are present in a directory, the command assumes is the microsoft/go repository,
where "go" is the submodule, and "patches" contains the list of patch files. The search can be
overridden using the "-c" argument.
`

// repoRootFlag can be passed to any subcommand to specify a particular Microsoft Go repository
// directory.
var repoRootFlag = flag.String("c", "", "Disable Go repository discovery and use this path as the target.")

var subcommands = []subcommand{
	apply,
	extract,
	rebase,
}

type subcommand struct {
	// The name of the subcommand.
	Name string
	// Summary is a description of the subcommand. Short, so it fits in a list of all subcommands in
	// help text.
	Summary string
	// Handle is called when the subcommand is the one picked by the user. It must set up additional
	// flags on its own, run flag parsing by calling parseFlagArgs, then carry out the subcommand.
	Handle func() error
}

func main() {
	if len(os.Args) < 2 || os.Args[1] == "-h" {
		printMainUsage()
		return
	}

	for _, subCmd := range subcommands {
		if subCmd.Name == os.Args[1] {
			err := subCmd.Handle()
			if err != nil {
				fmt.Printf("\n%v\n", err)
				os.Exit(1)
			}

			fmt.Println("\nSuccess.")
			return
		}
	}
	fmt.Fprintf(flag.CommandLine.Output(), "Error: Not a valid subcommand: %v\n\n", os.Args[1])
	printMainUsage()
	os.Exit(1)
}

func printMainUsage() {
	fmt.Fprintf(flag.CommandLine.Output(), "Usage:\n")
	for _, c := range subcommands {
		fmt.Fprintf(flag.CommandLine.Output(), "  git go-patch %v [-h] [...]\n", c.Name)
		fmt.Fprintf(flag.CommandLine.Output(), "    %v\n", c.Summary)
	}
	fmt.Fprintf(flag.CommandLine.Output(), "%v", description)
}

func parseFlagArgs(helpDescription string) error {
	flag.Usage = func() {
		fmt.Fprintf(flag.CommandLine.Output(), "Usage:\n")
		flag.PrintDefaults()
		fmt.Fprintf(flag.CommandLine.Output(), "\n%s", helpDescription)
	}

	// Ignore arg 1: subcommand name.
	if err := flag.CommandLine.Parse(os.Args[2:]); err != nil {
		return err
	}

	if len(flag.Args()) > 0 {
		flag.Usage()
		return fmt.Errorf("non-flag argument(s) provided but not accepted: %v", flag.Args())
	}
	return nil
}

func getStatusFileDir(rootDir string) string {
	return filepath.Join(rootDir, "eng", "artifacts", "go-patch")
}

func getPrePatchStatusFilePath(rootDir string) string {
	return filepath.Join(getStatusFileDir(rootDir), "HEAD_BEFORE_APPLY")
}

func getPostPatchStatusFilePath(rootDir string) string {
	return filepath.Join(getStatusFileDir(rootDir), "HEAD_AFTER_APPLY")
}

func readStatusFile(file string) (string, error) {
	content, err := os.ReadFile(file)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(content)), nil
}

// findOuterRepoRoot returns the repoRootFlag value, if defined. Otherwise, searches the current
// working directory and its ancestors for a directory that appears to be a Microsoft Go repository:
// one that contains directories "patches" and "go". This function should only be called after flags
// have been parsed.
func findOuterRepoRoot() (string, error) {
	if *repoRootFlag != "" {
		return *repoRootFlag, nil
	}

	cwd, err := os.Getwd()
	if err != nil {
		return "", err
	}

	var dir = cwd
	for {
		goInfo, err := os.Stat(filepath.Join(dir, "go"))
		if err != nil {
			if !errors.Is(err, os.ErrNotExist) {
				return "", err
			}
		}
		patchesInfo, err := os.Stat(filepath.Join(dir, "patches"))
		if err != nil {
			if !errors.Is(err, os.ErrNotExist) {
				return "", err
			}
		}

		if goInfo != nil && goInfo.IsDir() && patchesInfo != nil && patchesInfo.IsDir() {
			fmt.Printf("Found Microsoft Go directory at %v\n", dir)
			return dir, nil
		}

		parent := filepath.Dir(dir)
		// When we've hit the filesystem root, Dir goes no further.
		if dir == parent {
			return "", fmt.Errorf("no Microsoft Go root found in any ancestor of %v", cwd)
		}
		dir = parent
	}
}
