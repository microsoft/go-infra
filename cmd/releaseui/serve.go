// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"time"

	azdoworkitem "github.com/microsoft/go-infra/azdo/workitem"
	"github.com/microsoft/go-infra/cmd/releaseui/internal/azdopipeline"
	"github.com/microsoft/go-infra/cmd/releaseui/internal/githubclient"
	"github.com/microsoft/go-infra/cmd/releaseui/internal/goimages"
	"github.com/microsoft/go-infra/cmd/releaseui/internal/goinfra"
	releaseui "github.com/microsoft/go-infra/releaseui"
	"github.com/microsoft/go-infra/subcmd"
)

func init() {
	subcommands = append(subcommands, subcmd.Option{
		Name:        "serve",
		Summary:     "Start the local release management UI",
		Description: "\n\nThe dashboard supports go-images execution and guided go-infra patch releases.\n",
		Handle:      handleServe,
	})
}

func handleServe(parse subcmd.ParseFunc) error {
	listenAddress := flag.String("listen", "127.0.0.1:0", "Loopback address for the local HTTP server")
	noOpen := flag.Bool("no-open", false, "Do not automatically open the UI in the default browser")
	releaseWorkItem := flag.Int(
		"release-work-item", 0,
		"Azure DevOps work item ID to restore; 0 starts without a selected release",
	)
	if err := parse(); err != nil {
		return err
	}
	if *releaseWorkItem < 0 {
		return errors.New("release work item ID must not be negative")
	}

	tokenProvider := &azdopipeline.CachingTokenProvider{
		Provider: azdopipeline.AzureCLITokenProvider{Runner: azdopipeline.ExecCommandRunner{}},
		TTL:      5 * time.Minute,
	}

	github, err := githubclient.New("github.com", githubclient.ExecCommandRunner{})
	if err != nil {
		return err
	}
	goInfraService, err := goinfra.NewGitHubService(github)
	if err != nil {
		return err
	}
	workItems, err := azdoworkitem.NewClient(azdoworkitem.Config{
		BaseURL:      "https://dev.azure.com/devdiv",
		Project:      "DEVDIV",
		WorkItemType: "Issue",
	}, tokenProvider)
	if err != nil {
		return err
	}
	assignedTo, err := workItems.CurrentUser(context.Background())
	if err != nil {
		return err
	}
	processRunStore, err := releaseui.NewReleaseRunWorkItemStore(workItems, assignedTo)
	if err != nil {
		return err
	}
	goInfraProcess := goinfra.NewProcess(goInfraService)

	azureHTTPClient := &http.Client{Timeout: 3 * time.Minute}
	goImagesService, err := goimages.NewAzureService(azureHTTPClient, tokenProvider)
	if err != nil {
		return err
	}
	goImagesProcess := goimages.NewProcess(goImagesService)
	options := []releaseui.Option{
		releaseui.WithProcesses(goImagesProcess, goInfraProcess),
		releaseui.WithReleaseRunStore(processRunStore),
		releaseui.WithReleaseWorkItems(workItems),
	}
	if *releaseWorkItem > 0 {
		selected, err := workItems.Get(context.Background(), *releaseWorkItem)
		if err != nil {
			return fmt.Errorf("select release work item %d: %w", *releaseWorkItem, err)
		}
		options = append(options, releaseui.WithReleaseWorkItem(selected))
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	return releaseui.ListenAndServe(ctx, *listenAddress, func(launchURL string) {
		fmt.Printf("Release UI listening at %s\n", launchURL)
		fmt.Println("Go-images pipeline 1023 execution is available for normal, rollback, and dev/ test releases.")
		if *releaseWorkItem > 0 {
			fmt.Printf("Restored Azure DevOps work item: %d\n", *releaseWorkItem)
		}
		if !*noOpen {
			if err := releaseui.OpenBrowser(launchURL); err != nil {
				log.Printf("Unable to open browser automatically: %v", err)
			}
		}
	}, options...)
}
