// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package main

import (
	"log"

	"github.com/microsoft/go-infra/subcmd"
)

// version is the semver of this tool. Compared against the value in the config file (if any) to
// ensure that all users of the tool contributing to a given repo have a new enough version of the
// tool to support all patching features used in that repo.
//
// When adding a new feature to the azoras tool, make sure it is backward compatible and
// increment the patch number here.
const version = "v1.0.0"

const description = `
azoras is a tool that helps work with the ORAS CLI and Azure ACR.
The subcommands implement common workflows for image annotations and maintenance.
`

var subcommands []subcmd.Option

func main() {
	if err := subcmd.Run("azoras", description, subcommands); err != nil {
		log.Fatal(err)
	}
}
