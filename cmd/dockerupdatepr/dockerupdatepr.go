// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package main

import (
	"fmt"

	"github.com/microsoft/go-infra/buildmodel"
)

const description = `
Example: Create a temporary repo and create a commit that updates the repo to use the build listed
in the build asset JSON file:

  pwsh eng/run.ps1 dockerupdatepr -build-asset-json /home/me/downloads/assets.json -n

The example command above includes the "-n" dry run arg. Removing that arg makes the command submit
the change as a GitHub PR.

To run this command locally, it may be useful to specify Git addresses like
'git@github.com:microsoft/go' to use SSH authentication.

This script creates a temporary copy of the Go Docker repository in 'eng/artifacts/' by default.
`

func main() {
	f := buildmodel.CreateBoundPRFlags()

	buildmodel.ParseBoundFlags(description)

	if err := buildmodel.SubmitUpdatePR(f); err != nil {
		panic(err)
	}

	fmt.Println("\nSuccess.")
}
