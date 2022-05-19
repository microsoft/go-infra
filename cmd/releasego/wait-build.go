// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"time"

	"github.com/microsoft/azure-devops-go-api/azuredevops/build"
	"github.com/microsoft/go-infra/azdo"
	"github.com/microsoft/go-infra/subcmd"
)

func init() {
	subcommands = append(subcommands, subcmd.Option{
		Name:    "wait-build",
		Summary: "Wait until the AzDO Pipeline build is complete and successful.",
		Handle:  handleWaitBuild,
	})
}

func handleWaitBuild(p subcmd.ParseFunc) error {
	id := flag.Int("id", 0, "[Required] The AzDO build ID (not build number) to query.")
	pollDelaySeconds := flag.Int("poll-delay", 5, "Number of seconds to wait between each poll attempt.")
	azdoFlags := azdo.BindClientFlags()

	if err := p(); err != nil {
		return err
	}

	if *id == 0 {
		flag.Usage()
		log.Fatalln("No ID specified.")
	}
	if err := azdoFlags.EnsureAssigned(); err != nil {
		flag.Usage()
		return err
	}

	pollDelay := time.Duration(*pollDelaySeconds) * time.Second

	ctx := context.Background()

	c, err := build.NewClient(ctx, azdoFlags.NewConnection())
	if err != nil {
		return err
	}

	for {
		b, err := c.GetBuild(ctx, build.GetBuildArgs{
			BuildId: id,
			Project: azdoFlags.Proj,
		})
		if err != nil {
			return err
		}

		url, _ := azdo.GetBuildWebURL(b)

		if *b.Status != build.BuildStatusValues.Completed {
			log.Printf("Build status: %v, next poll in %v... %v\n", *b.Status, pollDelay, url)
			time.Sleep(pollDelay)
			continue
		}

		if *b.Result != build.BuildResultValues.Succeeded &&
			*b.Result != build.BuildResultValues.PartiallySucceeded {

			return fmt.Errorf("build completed, but was not successful: result %q %v", *b.Result, url)
		}

		log.Printf("Build completed! %v", url)
		log.Printf("Success. Result: %q\n", *b.Result)
		break
	}

	return nil
}
