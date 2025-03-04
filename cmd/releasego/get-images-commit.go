// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/microsoft/go-infra/azdo"
	"github.com/microsoft/go-infra/buildmodel/dockerversions"
	"github.com/microsoft/go-infra/gitcmd"
	"github.com/microsoft/go-infra/githubutil"
	"github.com/microsoft/go-infra/goversion"
	"github.com/microsoft/go-infra/subcmd"
)

func init() {
	subcommands = append(subcommands, subcmd.Option{
		Name:    "get-images-commit",
		Summary: "Get the commit in the go-images repository that contains the specified releases. If not, poll and wait for it to appear in the target branch",
		Handle:  handleGetImagesCommit,
	})
}

func handleGetImagesCommit(p subcmd.ParseFunc) error {
	versionsFlag := flag.String(
		"versions", "",
		"[Required] A list of full or partial microsoft/go version numbers (major.minor.patch[-revision[-suffix]]). Separated by commas.")
	repoFlag := flag.String("repo", "", "[required] The Git repo to check, as a full, cloneable URL.")
	branchFlag := flag.String("branch", "", "[required] The branch to check.")
	azdoVarName := flag.String("set-azdo-variable", "", "An AzDO variable name to set to the commit hash using a logging command.")
	keepTemp := flag.Bool("w", false, "Keep the temporary repository used for polling, rather than cleaning it up.")
	pollDelaySeconds := flag.Int("poll-delay", 5, "Number of seconds to wait between each poll attempt.")
	gitHubAuthFlags := *githubutil.BindGitHubAuthFlags("")

	if err := p(); err != nil {
		return err
	}

	if *versionsFlag == "" {
		return errors.New("no versions specified")
	}
	if *repoFlag == "" {
		return errors.New("no repo specified")
	}
	if *branchFlag == "" {
		return errors.New("no branch specified")
	}

	if *gitHubAuthFlags.GitHubAppClientID != "" {
		token, err := githubutil.GenerateInstallationToken(
			context.Background(),
			*gitHubAuthFlags.GitHubAppClientID,
			*gitHubAuthFlags.GitHubAppInstallation,
			*gitHubAuthFlags.GitHubAppPrivateKey,
		)
		if err != nil {
			return err
		}

		// Inject the token into the repo URL
		parts := strings.Split(*repoFlag, "https://")
		if len(parts) > 1 {
			*repoFlag = fmt.Sprintf("https://git:%s@%s", token, parts[1])
		} else {
			*repoFlag = fmt.Sprintf("https://git:%s@%s", token, *repoFlag)
		}
	}

	pollDelay := time.Duration(*pollDelaySeconds) * time.Second

	versions := strings.Split(*versionsFlag, ",")

	tempRepo, err := gitcmd.NewTempGitRepo()
	if err != nil {
		return err
	}
	if !*keepTemp {
		defer gitcmd.AttemptDelete(tempRepo)
	}

	checker := &imageVersionChecker{
		GitDir:   tempRepo,
		Upstream: *repoFlag,
		Branch:   *branchFlag,
		Versions: versions,
	}

	result := gitcmd.Poll(checker, pollDelay)
	if *azdoVarName != "" {
		azdo.LogCmdSetVariable(*azdoVarName, result)
	}
	return nil
}

// tagChecker checks for an upstream release by looking for a Git tag.
type imageVersionChecker struct {
	GitDir   string
	Upstream string
	Branch   string
	Versions []string
}

func (c *imageVersionChecker) Check() (string, error) {
	// Fetch the tip of the branch into local branch "check" to look at.
	if err := gitcmd.Run(c.GitDir, "fetch", "--depth", "1", c.Upstream, "+"+c.Branch+":check"); err != nil {
		return "", err
	}

	// Find the versionsModel.json file content and see if it has the releases listed.
	versionsJSON, err := gitcmd.Show(c.GitDir, "check:src/microsoft/versions.json")
	if err != nil {
		return "", err
	}

	var versionsModel dockerversions.Versions
	d := json.NewDecoder(strings.NewReader(versionsJSON))
	if err := d.Decode(&versionsModel); err != nil {
		return "", fmt.Errorf("unable to decode versions.json file: %w", err)
	}

	if err := c.CheckExpectedVersions(versionsModel); err != nil {
		return "", err
	}

	return gitcmd.RevParse(c.GitDir, "check")
}

// CheckExpectedVersions checks that a model contains all the versions expected by this checker.
func (c *imageVersionChecker) CheckExpectedVersions(model dockerversions.Versions) error {
	var allMissing []string

	for _, expected := range c.Versions {
		var found bool
		expectedVersion := goversion.New(expected).Full()

		for _, v := range model {
			modelVersion := v.GoVersion().Full()

			if expectedVersion == modelVersion {
				found = true
				break
			}
		}

		if !found {
			allMissing = append(allMissing, expectedVersion)
		}
	}

	if len(allMissing) > 0 {
		allFound := make([]string, 0, len(model))
		for _, v := range model {
			allFound = append(allFound, v.GoVersion().Full())
		}
		sort.Strings(allMissing)
		sort.Strings(allFound)
		return fmt.Errorf("missing versions: %v, found %v", allMissing, allFound)
	}
	return nil
}
