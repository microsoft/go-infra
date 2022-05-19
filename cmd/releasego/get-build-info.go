// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package main

import (
	"context"
	"flag"
	"fmt"
	"log"

	"github.com/microsoft/azure-devops-go-api/azuredevops/build"
	"github.com/microsoft/go-infra/azdo"
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
	prefix := flag.String("prefix", "", "The prefix to use before all env vars set by this command.")
	azdoFlags := azdo.BindClientFlags()

	if err := p(); err != nil {
		return err
	}

	if *id == 0 {
		flag.Usage()
		log.Fatalln("No build ID specified.")
	}
	if err := azdoFlags.EnsureAssigned(); err != nil {
		flag.Usage()
		return err
	}

	ctx := context.Background()
	c, err := build.NewClient(ctx, azdoFlags.NewConnection())
	if err != nil {
		return err
	}
	b, err := c.GetBuild(ctx, build.GetBuildArgs{
		Project:         azdoFlags.Proj,
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
