// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package main

import (
	"github.com/microsoft/go-infra/executil"
	"github.com/microsoft/go-infra/subcmd"
)

func init() {
	subcommands = append(subcommands, subcmd.Option{
		Name:    "stage-diff",
		Summary: "Stage the changes between two refs for review.",
		Description: `

This command helps review patch file changes. It checks out the 'before' branch (detached) but with
the stage set to the 'after' state. This makes changes show up in 'git diff --cached',
'git status', and in IDE-provided Git tools, in a way that is often easier to review than examining
the "diff of a diff" in a PR that touches patch files.

To use this command:
1. Check out the "before" state in the outer repo.
2. Run: git go-patch apply -before
3. Check out the "after" state in the outer repo.
4. Run: git go-patch apply -after
5. Run: git go-patch stage-diff

This command puts the submodule in a dirty state, so consider using "git go-patch apply -f" as soon
as the review is complete to clean up.

Note: this strategy doesn't preserve any information about which patch contributed which changes.
Therefore, this command is useful for reviewing functionality, but if a change involves multiple
patches, further review is needed to ensure each change is in the appropriate patch. This is best
reviewed in the PR.
` + repoRootSearchDescription,
		Handle: handleStageDiff,
	})
}

const (
	stageDiffBeforeBranch = "git-go-patch/stage-diff/before"
	stageDiffAfterBranch  = "git-go-patch/stage-diff/after"
)

func handleStageDiff(p subcmd.ParseFunc) error {
	if err := p(); err != nil {
		return err
	}

	config, err := loadConfig()
	if err != nil {
		return err
	}
	_, goDir := config.FullProjectRoots()

	// Set up stage.
	if err := executil.Dir(goDir, "git", "checkout", "--detach", stageDiffAfterBranch).Run(); err != nil {
		return err
	}

	// Move to the "before" commit, but keep the "after" stage.
	return executil.Dir(goDir, "git", "reset", "--soft", stageDiffBeforeBranch).Run()
}
