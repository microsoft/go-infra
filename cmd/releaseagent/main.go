// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package main

import (
	"flag"
	"fmt"
	"log"
	"slices"

	"github.com/microsoft/go-infra/cmd/releaseagent/internal/releasesteps"
	"github.com/microsoft/go-infra/goversion"
	"github.com/microsoft/go-infra/subcmd"
)

const description = `
releaseagent coordinates a release of Microsoft build of Go repository.
`

// subcommands is the list of subcommand options, populated by each file's init function.
var subcommands []subcmd.Option

func main() {
	if err := subcmd.Run("releasego", description, subcommands); err != nil {
		log.Fatal(err)
	}
}

func BindInputFlags() *releasesteps.Input {
	var i releasesteps.Input

	flag.Func(
		"version",
		"A version to release. Pass the flag multiple times to release multiple versions",
		func(s string) error {
			v := goversion.New(s) // Ensure it's a valid version and normalize.

			// Check for obvious mistakes.
			if v.Major != "1" {
				return fmt.Errorf("major version must be 1, got %q in %q", v.Major, v)
			}
			if slices.Contains(i.Versions, s) {
				return fmt.Errorf("duplicate version passed: %q", s)
			}

			i.Versions = append(i.Versions, v.Full())
			return nil
		})

	flag.BoolVar(&i.Security, "security", false, "This release contains security fixes. Changes announcement text")
	flag.StringVar(&i.RunnerGitHubUser, "runner", "", "GitHub username of the dev in charge of this release")

	flag.StringVar(
		&i.ReleaseConfigVariableGroup,
		"release-config-variable-group", "",
		"AzDO variable group containing the release configuration. Mostly secrets")

	return &i
}

func BindSecretFlags() *releasesteps.Secret {
	var s releasesteps.Secret
	flag.StringVar(&s.GitHubPAT, "github-pat", "", "GitHub PAT with write access to microsoft/go")
	flag.StringVar(&s.GitHubReviewerPAT, "github-reviewer-pat", "", "GitHub PAT for microsoft/go approver account")
	flag.StringVar(&s.AzDOPAT, "azdo-pat", "", "Azure DevOps Personal Access Token (typically System.AccessToken)")
	return &s
}
