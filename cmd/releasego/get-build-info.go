// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package main

import (
	"context"
	"flag"
	"fmt"
	"log"

	"github.com/microsoft/azure-devops-go-api/azuredevops"
	"github.com/microsoft/azure-devops-go-api/azuredevops/build"
	"github.com/microsoft/go-infra/subcmd"
)

func init() {
	subcommands = append(subcommands, subcmd.Option{
		Name:    "get-build-info",
		Summary: "Get info about an AzDO build by ID.",
		Description: `

Get info about the given AzDO build and set pipeline variables to transfer the data to future steps
in the release pipeline.

Sets variables with the given prefix for BuildNumber, SourceVersion, and SourceBranch.
`,
		Handle: handleGetBuildInfo,
	})
}

func handleGetBuildInfo(p subcmd.ParseFunc) error {
	id := flag.Int("id", 0, "[Required] The AzDO build ID (not build number) to query.")
	org := flag.String("org", "", "[Required] The AzDO organization URL.")
	proj := flag.String("proj", "", "[Required] The AzDO project URL.")
	azdoPAT := azdoPATFlag()
	prefix := flag.String("prefix", "", "The prefix to use before all env vars set by this command.")

	if err := p(); err != nil {
		return err
	}

	if *id == 0 {
		flag.Usage()
		log.Fatalln("No build ID specified.")
	}
	if *org == "" {
		log.Fatalln("No AzDO org specified.")
	}
	if *proj == "" {
		log.Fatalln("No AzDO project URL specified.")
	}
	if *azdoPAT == "" {
		log.Fatalln("No AzDO PAT specified.")
	}

	connection := azuredevops.NewPatConnection(*org, *azdoPAT)

	ctx := context.Background()
	c, err := build.NewClient(ctx, connection)
	if err != nil {
		return err
	}
	b, err := c.GetBuild(ctx, build.GetBuildArgs{
		Project:         proj,
		BuildId:         id,
		PropertyFilters: nil,
	})
	if err != nil {
		return err
	}

	set := func(name string, value *string) {
		if value == nil {
			log.Printf("Nil result found for %v\n", name)
			return
		}
		log.Printf("Found %v value: %q\n", name, *value)
		fmt.Printf("##vso[task.setvariable variable=%v]%v\n", *prefix+name, *value)
	}
	set("BuildNumber", b.BuildNumber)
	set("SourceVersion", b.SourceVersion)
	set("SourceBranch", b.SourceBranch)

	return nil
}
