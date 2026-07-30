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
	"sort"
	"time"

	"github.com/microsoft/go-infra/buildmodel/dockerversions"
	"github.com/microsoft/go-infra/cmd/releaseagent/internal/azdopipeline"
	"github.com/microsoft/go-infra/cmd/releaseagent/internal/azdorepo"
	"github.com/microsoft/go-infra/cmd/releaseagent/internal/goimagesdemo"
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
		Description: "\n\nThis iteration previews and simulates pipeline 1023. Optional Azure access is read-only.\n",
		Handle:      handleServe,
	})
}

func handleServe(parse subcmd.ParseFunc) error {
	listenAddress := flag.String("listen", "127.0.0.1:0", "Loopback address for the local HTTP server")
	noOpen := flag.Bool("no-open", false, "Do not automatically open the UI in the default browser")
	demoDelay := flag.Duration("demo-step-delay", 250*time.Millisecond, "Duration of each step in the safe simulation")
	sessionFile := flag.String("session-file", "", "Optional JSON file used to persist and restore the non-secret release plan")
	enableAzureReadOnly := flag.Bool("enable-go-images-azure-read-only", false, "Enable authenticated read-only discovery of pipeline 1023 runs")
	enableUnofficialDemo := flag.Bool("enable-go-images-unofficial-demo", false, "Enable confirmation-protected real execution of unofficial pipeline 1492 with dev/ publishing")
	if err := parse(); err != nil {
		return err
	}
	if *enableAzureReadOnly && *sessionFile == "" {
		return errors.New("-enable-go-images-azure-read-only requires -session-file")
	}
	if *enableUnofficialDemo && !*enableAzureReadOnly {
		return errors.New("-enable-go-images-unofficial-demo requires -enable-go-images-azure-read-only")
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
	if *enableAzureReadOnly {
		azureHTTPClient := &http.Client{Timeout: 3 * time.Minute}
		tokenProvider := &azdopipeline.CachingTokenProvider{
			Provider: azdopipeline.AzureCLITokenProvider{Runner: azdopipeline.ExecCommandRunner{}},
			TTL:      5 * time.Minute,
		}
		azureClient, err := azdopipeline.NewClient(
			"https://dev.azure.com/dnceng",
			"internal",
			azureHTTPClient,
			tokenProvider,
		)
		if err != nil {
			return err
		}
		repoClient, err := azdorepo.NewClient(
			"https://dev.azure.com/dnceng",
			"internal",
			"microsoft-go-images",
			azureHTTPClient,
			tokenProvider,
		)
		if err != nil {
			return err
		}
		versionResolver := goimagesrelease.VersionResolverFunc(func(ctx context.Context, commit string) ([]string, error) {
			var model dockerversions.Versions
			if err := repoClient.GetJSONFileAtCommit(ctx, "/src/microsoft/versions.json", commit, &model); err != nil {
				return nil, err
			}
			versions := make([]string, 0, len(model))
			for _, version := range model {
				versions = append(versions, version.GoVersion().Full())
			}
			sort.Strings(versions)
			return versions, nil
		})
		options = append(options, releaseui.WithGoImagesReadOnlyIntegration(releaseui.GoImagesReadOnlyIntegration{
			DefinitionID: 1023,
			Preflight: func(ctx context.Context) (string, error) {
				definition, err := azureClient.GetDefinition(ctx, 1023)
				if err != nil {
					return "", err
				}
				if definition.Name != "microsoft-go-images (official)" ||
					definition.QueueStatus != "enabled" ||
					definition.DefaultBranch != "refs/heads/microsoft/main" ||
					definition.Repository != "microsoft-go-images" ||
					definition.YAMLPath != "eng/pipeline/go-docker-rolling-internal-pipeline.yml" {

					return "", fmt.Errorf("pipeline 1023 does not match the read-only allowlist: %#v", definition)
				}
				pipelineYAML, err := repoClient.GetFileAtBranch(
					ctx,
					"/eng/pipeline/go-docker-rolling-internal-pipeline.yml",
					"refs/heads/microsoft/main",
				)
				if err != nil {
					return "", fmt.Errorf("read pipeline 1023 YAML: %w", err)
				}
				if err := goimagesrelease.ValidatePipelineParameterContract(pipelineYAML); err != nil {
					return "", fmt.Errorf("verify pipeline 1023 parameters: %w", err)
				}
				return "Authenticated and verified direct go-images pipeline 1023 and its runtime parameter contract. Access is read-only.", nil
			},
			FindRuns: func(ctx context.Context, versions []string) ([]releaseui.PipelineRunCandidate, error) {
				service, err := goimagesrelease.New(azureClient, goimagesrelease.Config{
					DefinitionID:    1023,
					Versions:        versions,
					VersionResolver: versionResolver,
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
					DefinitionID:    1023,
					Versions:        versions,
					VersionResolver: versionResolver,
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
			MonitorRun: func(ctx context.Context, buildID int, versions []string) error {
				service, err := goimagesrelease.New(azureClient, goimagesrelease.Config{
					DefinitionID:    1023,
					Versions:        versions,
					VersionResolver: versionResolver,
					PollInterval:    5 * time.Second,
				}, nil)
				if err != nil {
					return err
				}
				return service.MonitorRun(ctx, buildID)
			},
		}))
		if *enableUnofficialDemo {
			queueClient, err := goimagesdemo.NewHTTPQueueClient(
				"https://dev.azure.com/dnceng",
				"internal",
				azureHTTPClient,
				tokenProvider,
			)
			if err != nil {
				return err
			}
			validateUnofficialSource := func(ctx context.Context, commit string) error {
				pipelineYAML, err := repoClient.GetFileAtCommit(
					ctx,
					"/eng/pipeline/go-docker-rolling-internal-pipeline-unofficial.yml",
					commit,
				)
				if err != nil {
					return fmt.Errorf("read unofficial pipeline YAML at %s: %w", commit, err)
				}
				return goimagesrelease.ValidateUnofficialPipelineParameterContract(pipelineYAML)
			}
			options = append(options, releaseui.WithGoImagesUnofficialDemoIntegration(
				releaseui.GoImagesUnofficialDemoIntegration{
					DefinitionID: goimagesdemo.DefinitionID,
					Preflight: func(ctx context.Context) (string, error) {
						definition, err := azureClient.GetDefinition(ctx, goimagesdemo.DefinitionID)
						if err != nil {
							return "", err
						}
						if definition.Name != "microsoft-go-images (unofficial)" ||
							definition.QueueStatus != "enabled" ||
							definition.DefaultBranch != "refs/heads/microsoft/main" ||
							definition.Repository != "microsoft-go-images" ||
							definition.YAMLPath != "eng/pipeline/go-docker-rolling-internal-pipeline-unofficial.yml" {

							return "", fmt.Errorf("pipeline 1492 does not match the unofficial-demo allowlist: %#v", definition)
						}
						pipelineYAML, err := repoClient.GetFileAtBranch(
							ctx,
							"/eng/pipeline/go-docker-rolling-internal-pipeline-unofficial.yml",
							"refs/heads/microsoft/main",
						)
						if err != nil {
							return "", fmt.Errorf("read pipeline 1492 YAML: %w", err)
						}
						if err := goimagesrelease.ValidateUnofficialPipelineParameterContract(pipelineYAML); err != nil {
							return "", fmt.Errorf("verify pipeline 1492 parameters: %w", err)
						}
						return "Verified pipeline 1492. It performs a real build, test signing, and test-ACR publication under dev/.", nil
					},
					ValidateSource: validateUnofficialSource,
					NewService: func(request releaseui.GoImagesUnofficialDemoRequest) (releasesteps.GoImagesReleaseService, error) {
						return goimagesdemo.New(azureClient, queueClient, goimagesdemo.Config{
							SessionID:            request.SessionID,
							ExecutionDigest:      request.ExecutionDigest,
							Versions:             request.Versions,
							SourceBuildID:        request.SourceBuildID,
							SourceVersion:        request.SourceVersion,
							PollInterval:         5 * time.Second,
							PreviousQueueAttempt: request.PreviousQueueAttempt,
							ReconcileAttempts:    6,
							ReconcileInterval:    5 * time.Second,
						}, nil)
					},
				},
			))
		}
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
	if *enableUnofficialDemo {
		fmt.Println("Real unofficial pipeline 1492 demo is enabled with test-ACR dev/ publishing.")
	} else if *enableAzureReadOnly {
		fmt.Println("Read-only discovery of pipeline 1023 is enabled. Queueing is not implemented.")
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
		BuildID:       candidate.BuildID,
		DefinitionID:  candidate.DefinitionID,
		Status:        candidate.Status,
		Result:        candidate.Result,
		State:         string(candidate.State),
		URL:           candidate.URL,
		QueueTime:     candidate.QueueTime,
		SourceBranch:  candidate.SourceBranch,
		SourceVersion: candidate.SourceVersion,
		VersionSet:    candidate.VersionSet,
		Parameters:    candidate.Parameters,
	}
}
