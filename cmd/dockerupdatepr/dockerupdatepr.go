// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package main

import (
	"fmt"
	"log"

	"github.com/microsoft/go-infra/buildmodel"
)

const description = `
dockerupdatepr creates a PR that updates the Go Docker image repository to build images that contain
the build of Go specified by the given build asset JSON file.

Example dry run that prepares the update locally:

  go run ./cmd/dockerupdatepr -build-asset-json /home/me/downloads/assets.json -n

The "-n" is the dry run arg. Removing that arg makes the command submit the change as a GitHub PR.

This command creates a temporary copy of the Go Docker repository in 'eng/artifacts/' by default.

To run this command locally, it may be useful to specify Git addresses like
'git@github.com:microsoft/go' to use SSH authentication.
`

func main() {
	f := buildmodel.BindPRFlags()

	buildmodel.ParseBoundFlags(description)

	if err := buildmodel.SubmitUpdatePR(f); err != nil {
		log.Panic(err)
	}

	fmt.Println("\nSuccess.")
}
