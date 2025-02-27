// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package azurelinux

import (
	"fmt"
	"path"
	"strings"

	"github.com/microsoft/go-infra/buildmodel/buildassets"
)

const (
	AzureLinuxBuddyBuildURL           = "https://dev.azure.com/mariner-org/mariner/_build?definitionId=2190"
	AzureLinuxSourceTarballPublishURL = "https://dev.azure.com/mariner-org/mariner/_build?definitionId=2284"
)

func GeneratePRTitleFromAssets(assets *buildassets.BuildAssets, security bool) string {
	var b strings.Builder
	if security {
		b.WriteString("(security) ")
	}
	b.WriteString("golang: bump Go version to ")
	b.WriteString(assets.GoVersion().Full())
	return b.String()
}

func GeneratePRDescription(assets *buildassets.BuildAssets, latestMajor, security bool, notify string, prNumber int) string {
	// Use calls to fmt.Fprint* family for readability with consistency.
	// Ignore errors because they're acting upon a simple builder.
	var b strings.Builder
	fmt.Fprint(&b, "Hi! 👋 I'm the Microsoft team's bot. This is an automated pull request I generated to bump the Go version to ")
	fmt.Fprintf(&b, "[%s](%s).\n\n", assets.GoVersion().Full(), githubReleaseURL(assets))

	if security {
		fmt.Fprint(&b, "**This update contains security fixes.**\n\n")
	}

	fmt.Fprint(&b, "I'm not able to run the Azure Linux pipelines yet, so the Microsoft release runner will need to finalize this PR.")
	if notify != "" && notify != "ghost" {
		fmt.Fprintf(&b, " @%s", notify)
	}
	fmt.Fprint(&b, "\n\nFinalization steps:\n")

	printCopiableOption := func(name, value string) {
		fmt.Fprintf(&b, "  %s:  \n", name)
		fmt.Fprint(&b, "  ```\n")
		fmt.Fprintf(&b, "  %s\n", value)
		fmt.Fprint(&b, "  ```\n")
	}

	fmt.Fprintf(&b, "- Trigger [Source Tarball Publishing](%s) with:  \n", AzureLinuxSourceTarballPublishURL)
	printCopiableOption("Full Name", path.Base(assets.GoSrcURL))
	printCopiableOption("URL", githubReleaseDownloadURL(assets))

	fmt.Fprintf(&b, "- Trigger [the Buddy Build](%s) with:  \n", AzureLinuxBuddyBuildURL)
	if prNumber == 0 {
		// If we aren't able to update the PR later to put the PR number in, at least leave
		// instructions the release runner can follow.
		fmt.Fprint(&b, "  First field: `PR-` then the number of this PR.  \n")
	} else {
		printCopiableOption("First field", fmt.Sprintf("PR-%v", prNumber))
	}

	printCopiableOption("Core spec", golangSpecName(assets, latestMajor))

	fmt.Fprint(&b, "- Post a PR comment with the URL of the triggered Buddy Build.\n")
	fmt.Fprint(&b, "- Mark this draft PR as ready for review.\n")

	fmt.Fprint(&b, "\nThanks!\n")
	return b.String()
}
