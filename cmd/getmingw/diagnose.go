// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package main

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/microsoft/go-infra/executil"
	"github.com/microsoft/go-infra/subcmd"
)

func init() {
	subcommands = append(subcommands, subcmd.Option{
		Name:    "diagnose",
		Summary: "Print the current MinGW location and version based on finding gcc and clang in PATH.",
		Handle:  diagnose,
	})
}

func diagnose(p subcmd.ParseFunc) error {
	if err := p(); err != nil {
		return err
	}
	// Print PATH split by the path separator.
	fmt.Println(strings.Repeat("-", 80))
	fmt.Println("PATH entries:")
	var pathDirs []string
	for p := range strings.SplitSeq(os.Getenv("PATH"), string(os.PathListSeparator)) {
		fmt.Println("  " + p)
		pathDirs = append(pathDirs, p)
	}
	fmt.Println(strings.Repeat("-", 80))
	fmt.Println("PATHEXT:", os.Getenv("PATHEXT"))
	fmt.Println(strings.Repeat("-", 80))
	fmt.Println("Investigating each gcc/clang found in specific PATH entries.")
	// Look at each PATH entry and see if it contains gcc or clang.
	for _, dir := range pathDirs {
		printGccClangVersions(dir)
	}
	fmt.Println(strings.Repeat("-", 80))
	fmt.Println("Finding gcc/clang with ordinary PATH resolution.")
	printGccClangVersions("")
	fmt.Println(strings.Repeat("-", 80))
	// Print Go env.
	fmt.Println("Running go env:")
	if err := executil.Run(exec.Command("go", "env")); err != nil {
		fmt.Printf("Failed: %v\n", err)
	}
	return nil
}

func printGccClangVersions(dir string) {
	// Find gcc and clang and print their versions.
	for _, exe := range []string{"gcc", "clang"} {
		if dir != "" {
			exe = filepath.Join(dir, exe)
		}
		path, err := exec.LookPath(exe)
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				continue
			}
			fmt.Printf("LookPath failed: %v\n", err)
			continue
		}
		if err := executil.Run(exec.Command(path, "--version")); err != nil {
			fmt.Printf("Failed: %v\n", err)
		}
	}
}
