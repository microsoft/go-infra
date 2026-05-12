// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package main

import (
	"context"
	"flag"
	"log"
	"os"
	"strconv"

	"github.com/microsoft/azure-devops-go-api/azuredevops/build"
	"github.com/microsoft/go-infra/azdo"
	"github.com/microsoft/go-infra/subcmd"
)

func init() {
	subcommands = append(subcommands, subcmd.Option{
		Name:    "retain-build",
		Summary: "Mark an AzDO build to be retained forever (set keepForever=true).",
		Description: `
By default, retains the build that is currently running this command, using
BUILD_BUILDID, SYSTEM_COLLECTIONURI, and SYSTEM_TEAMPROJECT from the environment.
Pass -id, -org, or -proj to override.
`,
		Handle: handleRetainBuild,
	})
}

func handleRetainBuild(p subcmd.ParseFunc) error {
	id := flag.Int("id", 0, "The AzDO build ID to retain. Defaults to the current build (env BUILD_BUILDID).")
	azdoFlags := azdo.BindClientFlags()

	if err := p(); err != nil {
		return err
	}

	// Apply env-var defaults so the typical in-pipeline call site only needs to pass -azdopat.
	if *id == 0 {
		if v := os.Getenv("BUILD_BUILDID"); v != "" {
			parsed, err := strconv.Atoi(v)
			if err != nil {
				return err
			}
			*id = parsed
		}
	}
	if *azdoFlags.Org == "" {
		*azdoFlags.Org = azdo.GetEnvCollectionURI()
	}
	if *azdoFlags.Proj == "" {
		*azdoFlags.Proj = azdo.GetEnvProject()
	}

	if *id == 0 {
		flag.Usage()
		log.Fatalln("No build ID specified and BUILD_BUILDID env var is not set.")
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

	keepForever := true
	updated, err := c.UpdateBuild(ctx, build.UpdateBuildArgs{
		Build:   &build.Build{KeepForever: &keepForever},
		BuildId: id,
		Project: azdoFlags.Proj,
	})
	if err != nil {
		return err
	}

	url, _ := azdo.GetBuildWebURL(updated)
	log.Printf("Enabled permanent retention for build %v %v", *id, url)
	return nil
}
