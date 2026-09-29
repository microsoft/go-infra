// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"time"

	azdotoken "github.com/microsoft/go-infra/azdo/token"
	azdopipeline "github.com/microsoft/go-infra/cmd/releaseui/internal/azdo/pipeline"
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
	if err := parse(); err != nil {
		return err
	}

	tokenProvider := &azdotoken.CachingTokenProvider{
		Provider: azdotoken.AzureCLITokenProvider{Runner: azdotoken.ExecCommandRunner{}},
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
	goInfraProcess := goinfra.NewProcess(goInfraService)

	azureHTTPClient := &http.Client{Timeout: 3 * time.Minute}
	goImagesService, err := goimages.NewAzureService(azureHTTPClient, tokenProvider)
	if err != nil {
		return err
	}
	goImagesProcess := goimages.NewProcess(goImagesService)
	options := []releaseui.Option{
		releaseui.WithProcesses(goImagesProcess, goInfraProcess),
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	return releaseui.ListenAndServe(ctx, *listenAddress, func(launchURL string) {
		fmt.Printf("Release UI listening at %s\n", launchURL)
		fmt.Println("Go-images pipeline 1023 execution is available for normal, rollback, and dev/ test releases.")
		if !*noOpen {
			if err := releaseui.OpenBrowser(launchURL); err != nil {
				log.Printf("Unable to open browser automatically: %v", err)
			}
		}
	}, options...)
}
