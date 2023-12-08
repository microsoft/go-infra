// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package main

import (
	"errors"
	"flag"
	"fmt"
	"log"
	"os"
	"os/exec"
	"strings"
	"sync"

	"github.com/microsoft/go-infra/azdo"
	"github.com/microsoft/go-infra/subcmd"
)

func init() {
	subcommands = append(subcommands, subcmd.Option{
		Name:           "run",
		Summary:        "Download/extract/run with the specified MinGW in PATH.",
		TakeArgsReason: "The command to run. If not specified, runs 'gcc --version'.",
		Handle:         run,
	})
}

func run(p subcmd.ParseFunc) error {
	initFilterFlags()
	multi := flag.Bool("multi", false, "Run the command once per matching MinGW version rather than only match one version.")
	ciType := flag.String("ci", "", "In addition to the command, prepend to PATH in a CI-specific way. 'github-actions-env', 'azdo', or none.")
	if err := p(); err != nil {
		return err
	}

	if !(len(sources.Values) != 0) {
		return fmt.Errorf("must specify at least one source")
	}
	if !(len(versions.Values) != 0) {
		return fmt.Errorf("must specify at least one version")
	}
	if !*multi {
		if len(sources.Values) > 1 ||
			len(versions.Values) > 1 ||
			len(arches.Values) > 1 ||
			len(threadings.Values) > 1 ||
			len(exceptions.Values) > 1 ||
			len(runtimes.Values) > 1 ||
			len(llvms.Values) > 1 {
			return fmt.Errorf("multiple specified, but -multi not set")
		}
	}

	// Get the list of builds to use to run the command.
	builds, err := unmarshal()
	if err != nil {
		return err
	}
	matches := filter(builds)
	if len(matches) == 0 {
		return fmt.Errorf("no matching MinGW found")
	}
	if *multi {
		log.Printf("Using %v matches:\n", len(matches))
		for _, b := range matches {
			log.Printf("  %#v\n", b)
		}
	} else {
		if len(matches) > 1 {
			log.Printf("Found %v matches, expected just one. Add more parameters to constrain the search, or add '-multi'. Matches:", len(matches))
			for _, b := range matches {
				log.Printf("  %#v\n", b)
			}
			return fmt.Errorf("multiple matches found")
		}
		matches = matches[:1]
	}

	// Download (if necessary) up front.
	var wg sync.WaitGroup
	for _, b := range matches {
		b := b
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := b.GetOrCreateCacheBinDir(); err != nil {
				log.Panicf("failed to get %#v: %v", b.URL, err)
			}
		}()
	}
	wg.Wait()
	originalPath := os.Getenv("PATH")
	for _, b := range matches {
		binDir, err := b.GetOrCreateCacheBinDir()
		if err != nil {
			return err
		}
		// Set PATH so exec.Command's LookPath finds the right gcc if it's being called directly.
		newPath := strings.Join([]string{binDir, originalPath}, string(os.PathListSeparator))
		if err := os.Setenv("PATH", newPath); err != nil {
			return err
		}
		args := flag.CommandLine.Args()
		if len(args) == 0 {
			args = []string{"gcc", "--version"}
		}
		log.Printf("Running command with:\n  %v\n  %v", b.URL, binDir)
		cmd := exec.Command(args[0], args[1:]...)
		cmd.Env = append(os.Environ(), "PATH="+newPath)
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr
		if err := cmd.Run(); err != nil {
			log.Printf("Done, with error: %v", err)
		}
	}

	// Set PATH in specific types of CI.
	if *ciType != "" {
		binDir, err := matches[0].GetOrCreateCacheBinDir()
		if err != nil {
			return err
		}
		if *ciType == "github-actions-env" {
			// Append to the file specified by $GITHUB_PATH:
			// https://docs.github.com/en/actions/using-workflows/workflow-commands-for-github-actions#adding-a-system-path
			ghp := os.Getenv("GITHUB_PATH")
			if ghp == "" {
				return fmt.Errorf("GITHUB_PATH is not set")
			}
			f, err := os.OpenFile(ghp, os.O_APPEND|os.O_WRONLY, 0o666)
			if err != nil {
				if errors.Is(err, os.ErrNotExist) {
					f, err = os.Create(ghp)
					if err != nil {
						return err
					}
				} else {
					return err
				}
			}
			_, err = fmt.Fprintf(f, "%v\n", binDir)
			if errClose := f.Close(); err == nil {
				err = errClose
			}
			if err != nil {
				return err
			}
		} else if *ciType == "azdo" {
			azdo.LogCmdPrependPath(binDir)
		} else {
			return fmt.Errorf("unknown CI type %#q", *ciType)
		}
	}
	return nil
}
