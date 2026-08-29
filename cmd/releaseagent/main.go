// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package main

import (
	"log"

	"github.com/microsoft/go-infra/subcmd"
)

const description = `
releaseagent coordinates a release related to the Microsoft build of Go project.
`

// subcommands is the list of subcommand options, populated by each file's init function.
var subcommands []subcmd.Option

func main() {
	if err := subcmd.Run("releaseagent", description, subcommands); err != nil {
		log.Fatal(err)
	}
}
