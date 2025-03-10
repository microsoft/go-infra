// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package main

import (
	"context"
	"errors"
	"flag"
	"log"
	"time"

	"github.com/microsoft/go-infra/azdo"
	"github.com/microsoft/go-infra/buildreport"
	"github.com/microsoft/go-infra/githubutil"
	"github.com/microsoft/go-infra/subcmd"
)

func init() {
	subcommands = append(subcommands, subcmd.Option{
		Name:    "report",
		Summary: "Report release build's status to a GitHub issue",
		Description: `

This command uses the environment to find the AzDO Collection URI and Project to create web URLs for
builds being reported about. Other info about the build must be passed via the required flags.
`,
		Handle: handleReport,
	})
}

func handleReport(p subcmd.ParseFunc) error {
	repo := githubutil.BindRepoFlag()
	gitHubAuthFlags := githubutil.BindGitHubAuthFlags("")
	issue := flag.Int("i", 0, "[Required] The issue number to add the comment to.")

	status := flag.String(
		"build-status", "",
		"[Required] The current build status.\n"+
			"Converted to a symbol if it is an Agent.JobStatus value (e.g. 'Failed', 'Succeeded'), 'InProgress', or 'NotStarted'.")

	buildPipeline := flag.String("build-pipeline", "", "[Required] The name of the build pipeline.")
	buildID := flag.String("build-id", "", "[Required] The build ID to report.")

	start := flag.Bool("build-start", false, "Assign the current time as the start time of the reported build.")

	version := flag.String(
		"version", "",
		"A full microsoft/go version number (major.minor.patch-revision[-suffix]), if one applies.\n"+
			"This is used to categorize the list of builds in a release issue.")

	if err := p(); err != nil {
		return err
	}

	if *issue == 0 {
		return errors.New("no issue specified")
	}
	if *status == "" {
		return errors.New("no build-status specified")
	}
	if *buildPipeline == "" {
		return errors.New("no build-pipeline specified")
	}
	if *buildID == "" {
		return errors.New("no build-id specified")
	}

	owner, name, err := githubutil.ParseRepoFlag(repo)
	if err != nil {
		return err
	}

	s := buildreport.State{
		Version:    *version,
		Name:       *buildPipeline,
		ID:         *buildID,
		LastUpdate: time.Now().UTC(),
	}
	if *start {
		s.StartTime = s.LastUpdate
	}
	// Assume we're always reporting on a build in the same collection/project as the current build.
	s.URL = azdo.GetBuildURL(azdo.GetEnvCollectionURI(), azdo.GetEnvProject(), s.ID)

	buildStatus := azdo.GetEnvAgentJobStatus()
	if *status != "" {
		buildStatus = *status
	}

	switch buildStatus {
	// Handle possible AzDO env values.
	case "Succeeded", "SucceededWithIssues":
		s.Status = buildreport.SymbolSucceeded
	case "Canceled", "Failed":
		s.Status = buildreport.SymbolFailed
	// Handle values that are passed in manually, not provided by Agent.JobStatus.
	case "InProgress":
		s.Status = buildreport.SymbolInProgress
	case "NotStarted":
		s.Status = buildreport.SymbolNotStarted
	default:
		s.Status = buildStatus
	}

	log.Printf("Reporting %#v\n", s)
	ctx := context.Background()
	return buildreport.Update(ctx, owner, name, *gitHubAuthFlags, *issue, s)
}
