// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package main

import (
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/microsoft/go-infra/githubutil"
	"github.com/microsoft/go-infra/internal/azurelinux"
	"github.com/microsoft/go-infra/subcmd"
)

func init() {
	subcommands = append(subcommands, subcmd.Option{
		Name:    "update-azure-linux-local",
		Summary: "Update the Go spec files for Azure Linux.",
		Description: `
Updates the golang package spec file in a local copy of the Azure Linux repository. Uses the same
underlying update logic as update-azure-linux. Intended for testing and for local fixups when the
remote pipeline doesn't work.
`,
		Handle: updateAzureLinuxLocal,
	})
}

func updateAzureLinuxLocal(p subcmd.ParseFunc) error {
	var (
		buildAssetJSON string
		root           string
		latestMajor    bool
		security       bool
	)
	flag.StringVar(&buildAssetJSON, "build-asset-json", "assets.json", "The path of a build asset JSON file describing the Go build to update to.")
	flag.StringVar(&root, "root", "", "The path to the root of the Azure Linux repository. Uses current directory if not specified.")
	flag.BoolVar(&latestMajor, "latest-major", false, "This is the latest major version, so update 'golang.spec' instead of 'golang-1.<N>.spec'.")
	flag.BoolVar(&security, "security", false, "Whether to indicate in the PR title and description that this is a security release.")

	authorFlag := changelogAuthorFlag()

	if err := p(); err != nil {
		return err
	}

	author, err := changelogAuthor(*authorFlag)
	if err != nil {
		return err
	}

	start := time.Now()

	assets, err := loadBuildAssets(buildAssetJSON)
	if err != nil {
		return err
	}

	if root == "" {
		root = "."
	}
	// DirFS returns a fs.FS, but we know it also implements fs.ReadDirFS and fs.ReadFileFS so it
	// implements SimplifiedFS.
	fs := os.DirFS(root).(githubutil.SimplifiedFS)

	rm, err := azurelinux.ReadModel(fs)
	if err != nil {
		return err
	}
	v, err := rm.UpdateMatchingVersion(assets, latestMajor, start, author)
	if err != nil {
		return err
	}

	writeFile := func(path string, data []byte) error {
		// Joining the path has two purposes: not only do we need to look in root, but we also need
		// to convert the fs path (contains "/" even on Windows) to a file path.
		return os.WriteFile(filepath.Join(root, path), data, 0o666)
	}
	if err := errors.Join(
		writeFile(v.SpecPath, v.Spec),
		writeFile(v.SignaturesPath, v.Signatures),
		writeFile(azurelinux.CGManifestPath, rm.CGManifest),
	); err != nil {
		return fmt.Errorf("failed to write update result: %v", err)
	}

	fmt.Printf("Update complete!\n")
	fmt.Printf("PR title and commit summary would be: %s\n", azurelinux.GeneratePRTitleFromAssets(assets, security))
	fmt.Printf("PR description would be something like:\n---\n%s\n---\n", azurelinux.GeneratePRDescription(assets, latestMajor, security, "ghost", 1234))

	return nil
}
