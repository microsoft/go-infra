// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package main

import (
	"fmt"
	"log"
	"os"

	"github.com/microsoft/go-infra/buildmodel"
	"github.com/microsoft/go-infra/sync"
)

const description = `
Example: A sync operation dry run:

  go run ./cmd/sync -n

Sync runs a "merge from upstream" and submits it as a PR. This means fetching commits from an
upstream repo and merging them into corresponding branches in a target repo. This is configured in a
config file, by default 'eng/sync-config.json'. For each entry in the configuration:

1. Fetch each SourceBranch 'branch' from 'Upstream' to a local temp repository.
2. Fetch each 'microsoft/{branch}' from 'Target'.
3. Merge each upstream branch 'b' into corresponding 'microsoft/b'.
4. Push each merge commit to 'Head' (or 'Target' if 'Head' isn't specified) with a name that follows
   the pattern 'dev/auto-merge/microsoft/{branch}'.
5. Create a PR in 'Target' that merges the auto-merge branch. If the PR already exists, overwrite.
   (Force push.)

This script creates the temporary repository in 'eng/artifacts/' by default.

To run a subset of the syncs specified in the config file, or to swap out URLs for development
purposes, create a copy of the configuration file and point at it using a '-c' argument.
`

func main() {
	wd, err := os.Getwd()
	if err != nil {
		log.Panic(err)
	}
	f := sync.BindFlags(wd)

	buildmodel.ParseBoundFlags(description)

	if err := sync.MakePRs(f); err != nil {
		panic(err)
	}

	fmt.Println("\nSuccess.")
}
