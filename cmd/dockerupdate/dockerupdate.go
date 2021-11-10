// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package main

import (
	"flag"
	"fmt"
	"os"

	"github.com/microsoft/go-infra/buildmodel"
)

const description = `
dockerupdate updates a local Go Docker image repository to build images that contain the build of Go
specified by the given build asset JSON file.

Example: Update the existing repository in a specified directory to the new build listed in a
assets.json file that has been downloaded to the local machine:

  go run ./cmd/dockerupdate -d ~/git/go-images -build-asset-json ~/downloads/assets.json

This command is useful to update the Dockerfile contents e.g. when adding Dockerfiles for a new
branch or changing the Dockerfile templates. The 'dockerupdatepr' command could be used to do this,
but it has dev cycle overhead that is good to avoid.
`

func main() {
	f := buildmodel.BindUpdateFlags()
	d := flag.String("d", "", "The directory containing the Go Docker repository to update. If empty, uses the current directory.")

	buildmodel.ParseBoundFlags(description)

	if *d == "" {
		w, err := os.Getwd()
		if err != nil {
			panic(err)
		}
		d = &w
	}

	if err := buildmodel.RunUpdate(*d, f); err != nil {
		panic(err)
	}

	fmt.Println("\nSuccess.")
}
