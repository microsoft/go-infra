// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log"
	"slices"
	"strings"
	"time"

	"github.com/microsoft/azure-devops-go-api/azuredevops/git"
	"github.com/microsoft/azure-devops-go-api/azuredevops/pipelines"
	"github.com/microsoft/go-infra/azdo"
	"github.com/microsoft/go-infra/subcmd"
)

func init() {
	subcommands = append(subcommands, subcmd.Option{
		Name:    "wait-ci-trigger",
		Summary: "Wait for (or find) a build started by a specific commit's CI trigger.",
		Description: `

Search for a build in the specified pipeline that was started by a CI trigger on
a specified commit hash. If no build exists matching the criteria, poll until a
timeout is hit. If a build isn't found, fail.
`,
		Handle: handleWaitCITrigger,
	})
}

func handleWaitCITrigger(p subcmd.ParseFunc) error {
	pipelineID := flag.Int("pipeline-id", 0, "[Required] The AzDO pipeline ID to query.")
	repositoryID := flag.String("repository-id", "", "[Required] The AzDO repository ID to query. GUID or name.")
	commit := flag.String("commit", "", "[Required] The commit hash.")

	setVariable := flag.String("set-azdo-variable", "", "An AzDO variable name to set to the ID of the discovered build.")
	timeout := flag.Duration("timeout", 5*time.Minute, "How long to wait for a build to be found before failing.")
	timeBetween := flag.Duration("time-between-retries", 5*time.Second, "How long to wait between retries.")

	azdoFlags := azdo.BindClientFlags()

	if err := p(); err != nil {
		return err
	}

	if *pipelineID == 0 {
		flag.Usage()
		return errors.New("no pipeline ID specified")
	}
	if *commit == "" {
		flag.Usage()
		return errors.New("no commit specified")
	}
	if err := azdoFlags.EnsureAssigned(); err != nil {
		flag.Usage()
		return err
	}

	ctx, cancel := context.WithTimeout(context.Background(), *timeout)
	defer cancel()

	g, err := git.NewClient(ctx, azdoFlags.NewConnection())
	if err != nil {
		return err
	}

	pc := pipelines.NewClient(ctx, azdoFlags.NewConnection())
	pipeline, err := pc.GetPipeline(ctx, pipelines.GetPipelineArgs{
		Project:    azdoFlags.Proj,
		PipelineId: pipelineID,
	})
	if err != nil {
		return err
	}
	log.Printf("Found pipeline %q (%d)\n", *pipeline.Name, *pipeline.Id)

	trueBool := true
	topLimit := 1000 // Same as default, but specify to be sure it doesn't change.

	for {
		if ctx.Err() != nil {
			return ctx.Err()
		}

		statusesPtr, err := g.GetStatuses(ctx, git.GetStatusesArgs{
			Project:      azdoFlags.Proj,
			RepositoryId: repositoryID,
			CommitId:     commit,
			LatestOnly:   &trueBool,
			Top:          &topLimit,
		})
		if err != nil {
			return err
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}

		statuses := *statusesPtr
		if len(statuses) == topLimit {
			return fmt.Errorf(
				"found %d statuses for commit %q, which is the maximum number that could be returned. "+
					"There may be more. It's not expected that there are this many statuses and this is likely a deeper problem.",
				topLimit, *commit)
		}

		statuses = slices.DeleteFunc(statuses, func(s git.GitStatus) bool {
			if s.Context == nil {
				return false
			}
			return *s.Context.Name != "build/"+*pipeline.Name
		})
		if len(statuses) == 0 {
			log.Printf("No status found. Trying again after %v...", *timeBetween)
			time.Sleep(*timeBetween)
			continue
		}

		status := statuses[0]
		log.Printf(
			"Found build status %q for commit %q with targetUrl %q\n",
			*status.Context.Name, *commit, *status.TargetUrl)

		buildID, ok := strings.CutPrefix(*status.TargetUrl, "vstfs:///Build/Build/")
		if !ok {
			return fmt.Errorf("unexpected prefix in targetUrl %q", *status.TargetUrl)
		}

		log.Printf("Found build ID %q\n", buildID)
		// For any humans watching, create a clickable link.
		log.Printf(
			"Build link: %v%v/_build/results?buildId=%v",
			*azdoFlags.Org, *azdoFlags.Proj, buildID,
		)
		if *setVariable != "" {
			azdo.LogCmdSetVariable(*setVariable, buildID)
		}

		return nil
	}
}
