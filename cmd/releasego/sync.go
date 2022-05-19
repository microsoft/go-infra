// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package main

import (
	"errors"
	"flag"
	"fmt"
	"log"
	"os"
	"strconv"
	"strings"

	"github.com/microsoft/go-infra/azdo"
	"github.com/microsoft/go-infra/githubutil"
	"github.com/microsoft/go-infra/goversion"
	"github.com/microsoft/go-infra/subcmd"
	"github.com/microsoft/go-infra/sync"
)

func init() {
	subcommands = append(subcommands, subcmd.Option{
		Name:    "sync",
		Summary: "Sync the right branch to the specified release.",
		Description: `

This command ensures the repository either has a commit that will build the specified upstream
release, or creates an open PR that updates a branch's submodule to the correct commit.

The command reports the result by setting AzDO variables named by each '-set-*' flag.

If opening a PR is necessary, uses the upstream sync infrastructure:
github.com/microsoft/go-infra/cmd/sync.

If using the default temp directory for sync PR generation, run this command inside the root of a
Git clone of the go-infra repository. The default directory is stored in 'eng/artifacts'.
`,
		Handle: handleSync,
	})
}

func handleSync(p subcmd.ParseFunc) error {
	repo := githubutil.BindRepoFlag()
	version := flag.String(
		"version", "",
		"[Required] A full microsoft/goversion number (major.minor.patch-revision[-suffix]).\n"+
			"The configuration file is filtered to a single entry and branch using this info.")

	commit := flag.String("commit", "", "The upstream commit to update to.")

	setVariablePRNumber := flag.String(
		"set-azdo-variable-pr-number", "",
		"An AzDO variable name to set to the sync PR number, or nil if no sync PR is created.")
	setVariableUpToDateCommit := flag.String(
		"set-azdo-variable-up-to-date-commit", "",
		"An AzDO variable name to set to nil if a sync PR is created, otherwise the full commit hash that was found to be already up to date.")

	wd, err := os.Getwd()
	if err != nil {
		return err
	}
	syncFlags := sync.BindFlags(wd)

	if err := p(); err != nil {
		return err
	}

	if *version == "" {
		return errors.New("no version specified")
	}

	entries, err := syncFlags.ReadConfig()
	if err != nil {
		return err
	}
	v := goversion.New(*version)
	versionUpstream := versionBranch(v)

	foundEntry, foundTarget, err := findTarget(entries, *repo, versionUpstream)
	if err != nil {
		return err
	}

	if foundEntry == nil {
		return fmt.Errorf("unable to find config entry matching %q for version %q", versionUpstream, v.Full())
	}

	// Remove all branch mappings other than the one we want to sync.
	foundEntry.BranchMap = map[string]string{versionUpstream: foundTarget}
	if *commit != "" {
		// Use the target commit, not just what happens to be the latest.
		foundEntry.SourceBranchLatestCommit = map[string]string{versionUpstream: *commit}
	}

	dir, err := syncFlags.MakeGitWorkDir()
	if err != nil {
		return err
	}

	results, err := sync.MakeBranchPRs(syncFlags, dir, foundEntry)
	if err != nil {
		return err
	}

	if len(results) != 1 {
		return fmt.Errorf("expected one result, got %v: %v", len(results), results)
	}

	r := results[0]

	if r.Commit == "" {
		return fmt.Errorf("commit string empty in sync result %v", r)
	}

	if r.PR == nil {
		log.Printf("No PR created for commit: %v\n", r.Commit)
		if *setVariablePRNumber != "" {
			azdo.SetPipelineVariable(*setVariablePRNumber, "nil")
		}
		if *setVariableUpToDateCommit != "" {
			azdo.SetPipelineVariable(*setVariableUpToDateCommit, r.Commit)
		}
	} else {
		log.Printf("Created PR: %v\n", r.PR.Number)
		if *setVariablePRNumber != "" {
			azdo.SetPipelineVariable(*setVariablePRNumber, strconv.Itoa(r.PR.Number))
		}
		if *setVariableUpToDateCommit != "" {
			azdo.SetPipelineVariable(*setVariableUpToDateCommit, "nil")
		}
	}

	return nil
}

// findTarget searches through entries to find a config entry matching the given repo and upstream
// branch. Returns the found config entry and the target branch, nil if none is found, or an error
// if multiple matches are found.
func findTarget(entries []sync.ConfigEntry, repo string, upstream string) (*sync.ConfigEntry, string, error) {
	var foundEntry *sync.ConfigEntry
	var foundTarget string
	for i := range entries {
		entry := &entries[i]
		if !strings.HasSuffix(entry.Target, repo) {
			continue
		}
		for u, target := range entry.BranchMap {
			if u != upstream {
				continue
			}
			if foundEntry != nil {
				return nil, "", fmt.Errorf(
					"found entry matching target repo and upstream branch %q %q targeting %q, but already found %q targeting %q",
					entry.Target, u, target,
					foundEntry.Target, foundTarget)
			}
			foundEntry = entry
			foundTarget = target
		}
	}
	return foundEntry, foundTarget, nil
}
