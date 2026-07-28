// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"time"

	"github.com/microsoft/go-infra/cmd/releaseagent/internal/azdopipeline"
	"github.com/microsoft/go-infra/cmd/releaseagent/internal/goimagesrelease"
	"github.com/microsoft/go-infra/cmd/releaseagent/internal/releasesteps"
	"github.com/microsoft/go-infra/cmd/releaseagent/internal/releaseui"
	"github.com/microsoft/go-infra/cmd/releaseagent/internal/session"
	"github.com/microsoft/go-infra/subcmd"
)

func init() {
	subcommands = append(subcommands, subcmd.Option{
		Name:        "serve",
		Summary:     "Start the local go-images release pipeline planning UI",
		Description: "\n\nThis iteration previews and simulates pipeline 1151. It performs no external operations.\n",
		Handle:      handleServe,
	})
}

func handleServe(parse subcmd.ParseFunc) error {
	listenAddress := flag.String("listen", "127.0.0.1:0", "Loopback address for the local HTTP server")
	noOpen := flag.Bool("no-open", false, "Do not automatically open the UI in the default browser")
	demoDelay := flag.Duration("demo-step-delay", 250*time.Millisecond, "Duration of each step in the safe simulation")
	sessionFile := flag.String("session-file", "", "Optional JSON file used to persist and restore the non-secret release plan")
	enableSmoke := flag.Bool("enable-go-images-smoke-test", false, "Enable confirmation-protected one-time queueing of pipeline 1151 with all release actions disabled")
	smokeVariableGroup := flag.String("go-images-smoke-variable-group", "", "The only variable group accepted by the enabled pipeline 1151 smoke test")
	if err := parse(); err != nil {
		return err
	}
	if *enableSmoke && *sessionFile == "" {
		return errors.New("-enable-go-images-smoke-test requires -session-file")
	}
	if *enableSmoke && *smokeVariableGroup == "" {
		return errors.New("-enable-go-images-smoke-test requires -go-images-smoke-variable-group")
	}

	var options []releaseui.Option
	options = append(options, releaseui.WithDemoDelay(*demoDelay))
	var sessionPath string
	if *sessionFile != "" {
		store, err := session.NewFileStore(*sessionFile)
		if err != nil {
			return err
		}
		lease, err := store.AcquireLease()
		if err != nil {
			return err
		}
		defer func() {
			if err := lease.Release(); err != nil {
				log.Printf("Unable to release session lease: %v", err)
			}
		}()
		sessionPath = store.Path()
		options = append(options, releaseui.WithSessionStore(store))
	}
	if *enableSmoke {
		azureHTTPClient := &http.Client{Timeout: 3 * time.Minute}
		azureClient, err := azdopipeline.NewClient(
			"https://dev.azure.com/dnceng",
			"internal",
			azureHTTPClient,
			azdopipeline.AzureCLITokenProvider{Runner: azdopipeline.ExecCommandRunner{}},
		)
		if err != nil {
			return err
		}
		options = append(options, releaseui.WithGoImagesSmokeExecution(releaseui.GoImagesSmokeExecution{
			DefinitionID:  1151,
			VariableGroup: *smokeVariableGroup,
			Preflight: func(ctx context.Context) (string, error) {
				definition, err := azureClient.GetDefinition(ctx, 1151)
				if err != nil {
					return "", err
				}
				if definition.Name != "microsoft-go-infra-release-go-images (official)" ||
					definition.QueueStatus != "enabled" ||
					definition.DefaultBranch != "refs/heads/main" ||
					definition.YAMLPath != "eng/pipelines/release-go-images-pipeline.yml" {

					return "", fmt.Errorf("pipeline 1151 does not match the smoke-test allowlist: %#v", definition)
				}
				return "Authenticated and verified production pipeline 1151. All release actions are forced off.", nil
			},
			NewService: func(request releaseui.GoImagesServiceRequest) (releasesteps.GoImagesReleaseService, error) {
				return goimagesrelease.New(azureClient, goimagesrelease.Config{
					DefinitionID:    1151,
					SessionID:       request.SessionID,
					Versions:        request.Versions,
					ExecutionDigest: request.ExecutionDigest,
					PollInterval:    5 * time.Second,
				}, nil)
			},
			FindRuns: func(ctx context.Context, versions []string) ([]releaseui.PipelineRunCandidate, error) {
				service, err := goimagesrelease.New(azureClient, goimagesrelease.Config{
					DefinitionID:    1151,
					SessionID:       "discovery-only",
					Versions:        versions,
					ExecutionDigest: "discovery-only",
				}, nil)
				if err != nil {
					return nil, err
				}
				candidates, err := service.FindCandidates(ctx)
				if err != nil {
					return nil, err
				}
				result := make([]releaseui.PipelineRunCandidate, 0, len(candidates))
				for _, candidate := range candidates {
					result = append(result, convertRunCandidate(candidate))
				}
				return result, nil
			},
			ValidateRun: func(ctx context.Context, buildID int, versions []string) (releaseui.PipelineRunCandidate, error) {
				service, err := goimagesrelease.New(azureClient, goimagesrelease.Config{
					DefinitionID:    1151,
					SessionID:       "discovery-only",
					Versions:        versions,
					ExecutionDigest: "discovery-only",
				}, nil)
				if err != nil {
					return releaseui.PipelineRunCandidate{}, err
				}
				candidate, err := service.ValidateCandidate(ctx, buildID)
				if err != nil {
					return releaseui.PipelineRunCandidate{}, err
				}
				return convertRunCandidate(candidate), nil
			},
		}))
	}

	listener, err := net.Listen("tcp", *listenAddress)
	if err != nil {
		return fmt.Errorf("listen on %q: %w", *listenAddress, err)
	}
	defer listener.Close()
	if !listener.Addr().(*net.TCPAddr).IP.IsLoopback() {
		return fmt.Errorf("refusing to serve release UI on non-loopback address %q", listener.Addr())
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	ui, err := releaseui.New(ctx, options...)
	if err != nil {
		return err
	}
	baseURL := "http://" + listener.Addr().String()
	launchURL, err := ui.LaunchURL(baseURL)
	if err != nil {
		return err
	}

	server := &http.Server{
		Handler:           ui.Handler(),
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       2 * time.Minute,
		BaseContext: func(net.Listener) context.Context {
			return ctx
		},
	}
	serverResult := make(chan error, 1)
	go func() {
		serverResult <- server.Serve(listener)
	}()

	fmt.Printf("Release UI listening at %s\n", launchURL)
	if *enableSmoke {
		fmt.Println("One-time pipeline 1151 smoke execution is enabled with every release action forced off.")
	} else {
		fmt.Println("Go-images pipeline execution is disabled; this UI can only plan and simulate it.")
	}
	if sessionPath != "" {
		fmt.Printf("Durable session file: %s\n", sessionPath)
	}
	if !*noOpen {
		if err := releaseui.OpenBrowser(launchURL); err != nil {
			log.Printf("Unable to open browser automatically: %v", err)
		}
	}

	select {
	case <-ctx.Done():
	case err := <-serverResult:
		if !errors.Is(err, http.ErrServerClosed) {
			return fmt.Errorf("serve release UI: %w", err)
		}
	}

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := server.Shutdown(shutdownCtx); err != nil {
		return fmt.Errorf("shut down release UI: %w", err)
	}
	return nil
}

func convertRunCandidate(candidate goimagesrelease.Candidate) releaseui.PipelineRunCandidate {
	return releaseui.PipelineRunCandidate{
		BuildID:         candidate.BuildID,
		Status:          candidate.Status,
		Result:          candidate.Result,
		State:           string(candidate.State),
		URL:             candidate.URL,
		QueueTime:       candidate.QueueTime,
		SessionID:       candidate.SessionID,
		VersionSet:      candidate.VersionSet,
		ExecutionDigest: candidate.ExecutionDigest,
		CreatedByUI:     candidate.CreatedByUI,
		Parameters:      candidate.Parameters,
	}
}
