// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package main

import (
	"os"
	"os/exec"
	"path/filepath"

	"github.com/microsoft/go-infra/executil"
	"github.com/microsoft/go-infra/subcmd"
)

type rebaseCmd struct{}

func (r rebaseCmd) Name() string {
	return "rebase"
}

func (r rebaseCmd) Summary() string {
	return "Run 'git rebase -i' on the commits created by 'apply'."
}

func (r rebaseCmd) Description() string {
	return `

This command rebases the commits applied to the submodule based on patch files. It uses the HEAD
commit recorded by "apply" as the base commit.

You can use this command to apply fixup and squash commits generated by the "git commit" args
"--fixup" and "--squash". To do this, configure Git using "git config --global rebase.autoSquash 1"
before running this command.

Be aware that editing earlier patch files may cause conflicts with later patch files.
` + repoRootSearchDescription
}

func (r rebaseCmd) Handle(p subcmd.ParseFunc) error {
	if err := p(); err != nil {
		return err
	}

	rootDir, err := findOuterRepoRoot()
	if err != nil {
		return err
	}

	goDir := filepath.Join(rootDir, "go")

	since, err := readStatusFile(getPrePatchStatusFilePath(rootDir))
	if err != nil {
		return err
	}

	cmd := exec.Command("git", "rebase", "-i", since)
	cmd.Stdin = os.Stdin
	cmd.Dir = goDir

	return executil.Run(cmd)
}
