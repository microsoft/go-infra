// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"sort"
	"time"

	"github.com/microsoft/go-infra/buildmodel/dockerversions"
	"github.com/microsoft/go-infra/cmd/releaseagent/internal/azdopipeline"
	"github.com/microsoft/go-infra/cmd/releaseagent/internal/azdorepo"
	"github.com/microsoft/go-infra/cmd/releaseagent/internal/goimagesexecution"
	"github.com/microsoft/go-infra/cmd/releaseagent/internal/goimagesrelease"
	"github.com/microsoft/go-infra/cmd/releaseagent/internal/goinfragithub"
	"github.com/microsoft/go-infra/cmd/releaseagent/internal/releasesteps"
	"github.com/microsoft/go-infra/cmd/releaseagent/internal/releaseui"
	"github.com/microsoft/go-infra/cmd/releaseagent/internal/session"
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
	configDir, err := os.UserConfigDir()
	if err != nil {
		return fmt.Errorf("resolve user configuration directory: %w", err)
	}
	listenAddress := flag.String("listen", "127.0.0.1:0", "Loopback address for the local HTTP server")
	noOpen := flag.Bool("no-open", false, "Do not automatically open the UI in the default browser")
	sessionFile := flag.String(
		"session-file",
		filepath.Join(configDir, "microsoft-go", "release-session.json"),
		"JSON file used to persist and restore the non-secret release plan",
	)
	enableGoInfraGitHub := flag.Bool("enable-go-infra-github-execution", false, "Enable two-step-confirmed go-infra labeling and workflow dispatch through the authenticated GitHub CLI")
	if err := parse(); err != nil {
		return err
	}
	if err := validateServeOptions(*sessionFile, *enableGoInfraGitHub); err != nil {
		return err
	}

	var options []releaseui.Option
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
	sessionPath := store.Path()
	options = append(options, releaseui.WithSessionStore(store))

	if *enableGoInfraGitHub {
		service, err := goinfragithub.New(goinfragithub.ExecCommandRunner{})
		if err != nil {
			return err
		}
		actionStore, err := releaseui.NewGoInfraActionFileStore(sessionPath + ".go-infra-action.json")
		if err != nil {
			return err
		}
		options = append(options,
			releaseui.WithGoInfraActionStore(actionStore),
			releaseui.WithGoInfraGitHubIntegration(releaseui.GoInfraGitHubIntegration{
				Preflight: service.Preflight,
				GetPullRequest: func(ctx context.Context, number int) (releaseui.GoInfraPullRequest, error) {
					pullRequest, err := service.GetPullRequest(ctx, number)
					return releaseui.GoInfraPullRequest{
						Number: pullRequest.Number, Title: pullRequest.Title, URL: pullRequest.URL,
						BaseRef: pullRequest.BaseRef, HeadRef: pullRequest.HeadRef, HeadSHA: pullRequest.HeadSHA,
						Fork: pullRequest.Fork, Labels: append([]string(nil), pullRequest.Labels...),
					}, err
				},
				AddReleaseOnMergeLabel: func(ctx context.Context, number int, expectedHeadSHA string) (releaseui.GoInfraPullRequest, error) {
					pullRequest, err := service.AddReleaseOnMergeLabel(ctx, number, expectedHeadSHA)
					return releaseui.GoInfraPullRequest{
						Number: pullRequest.Number, Title: pullRequest.Title, URL: pullRequest.URL,
						BaseRef: pullRequest.BaseRef, HeadRef: pullRequest.HeadRef, HeadSHA: pullRequest.HeadSHA,
						Fork: pullRequest.Fork, Labels: append([]string(nil), pullRequest.Labels...),
					}, err
				},
				DispatchPatchRelease: service.DispatchPatchRelease,
			}),
		)
	}

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
	resolveCurrentSource := func(ctx context.Context) (releaseui.GoImagesSource, error) {
		tip, err := repoClient.GetBranchTip(ctx, "refs/heads/microsoft/main")
		if err != nil {
			return releaseui.GoImagesSource{}, err
		}
		pipelineYAML, err := repoClient.GetFileAtCommit(
			ctx,
			"/eng/pipeline/go-docker-rolling-internal-pipeline.yml",
			tip.ObjectID,
		)
		if err != nil {
			return releaseui.GoImagesSource{}, fmt.Errorf("read pipeline 1023 YAML at %s: %w", tip.ObjectID, err)
		}
		if err := goimagesrelease.ValidatePipelineParameterContract(pipelineYAML); err != nil {
			return releaseui.GoImagesSource{}, fmt.Errorf("verify pipeline 1023 parameters at %s: %w", tip.ObjectID, err)
		}
		versions, err := versionResolver.VersionsAtCommit(ctx, tip.ObjectID)
		if err != nil {
			return releaseui.GoImagesSource{}, fmt.Errorf("read versions at %s: %w", tip.ObjectID, err)
		}
		return releaseui.GoImagesSource{Branch: tip.Name, Commit: tip.ObjectID, Versions: versions}, nil
	}
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
			return "Authenticated and verified direct go-images pipeline 1023. Source resolution and rollback validation are read-only.", nil
		},
		ResolveCurrentSource: resolveCurrentSource,
		ValidateRollback: func(ctx context.Context, buildID int) (releaseui.GoImagesRollbackSource, error) {
			candidate, err := goimagesrelease.ValidateRollbackSource(ctx, azureClient, versionResolver, 1023, buildID)
			if err != nil {
				return releaseui.GoImagesRollbackSource{}, err
			}
			var versions []string
			if err := json.Unmarshal([]byte(candidate.VersionSet), &versions); err != nil {
				return releaseui.GoImagesRollbackSource{}, fmt.Errorf("decode rollback version set: %w", err)
			}
			return releaseui.GoImagesRollbackSource{
				BuildID: candidate.BuildID, URL: candidate.URL, SourceBranch: candidate.SourceBranch,
				SourceVersion: candidate.SourceVersion, Versions: versions,
			}, nil
		},
		ListOngoing: func(ctx context.Context) ([]releaseui.GoImagesOngoingRun, error) {
			builds, err := azureClient.ListRecent(ctx, 1023)
			if err != nil {
				return nil, err
			}
			result := make([]releaseui.GoImagesOngoingRun, 0)
			for _, build := range builds {
				state, err := build.State()
				if err != nil {
					return nil, fmt.Errorf("interpret pipeline 1023 build %d: %w", build.ID, err)
				}
				if state != azdopipeline.RunStateWaiting && state != azdopipeline.RunStateRunning {
					continue
				}
				result = append(result, releaseui.GoImagesOngoingRun{
					BuildID: build.ID, Mode: goImagesModeFromBuild(build), Status: string(state),
					URL: build.WebURL, Queued: build.QueueTime,
				})
			}
			return result, nil
		},
	}))
	queueClient, err := goimagesexecution.NewHTTPQueueClient(
		"https://dev.azure.com/dnceng",
		"internal",
		azureHTTPClient,
		tokenProvider,
	)
	if err != nil {
		return err
	}
	options = append(options, releaseui.WithGoImagesExecutionIntegration(
		releaseui.GoImagesExecutionIntegration{
			DefinitionID: goimagesexecution.DefinitionID,
			Preflight: func(ctx context.Context) (string, error) {
				definition, err := azureClient.GetDefinition(ctx, goimagesexecution.DefinitionID)
				if err != nil {
					return "", err
				}
				if definition.Name != "microsoft-go-images (official)" ||
					definition.QueueStatus != "enabled" ||
					definition.DefaultBranch != "refs/heads/microsoft/main" ||
					definition.Repository != "microsoft-go-images" ||
					definition.YAMLPath != "eng/pipeline/go-docker-rolling-internal-pipeline.yml" {

					return "", fmt.Errorf("pipeline 1023 does not match the execution allowlist: %#v", definition)
				}
				return "Verified pipeline 1023. Normal and rollback publish to public/; test publishes to dev/.", nil
			},
			NewService: func(request releaseui.GoImagesExecutionRequest) (releasesteps.GoImagesReleaseService, error) {
				return goimagesexecution.New(azureClient, queueClient, goimagesexecution.Config{
					Mode:                 request.Mode,
					SessionID:            request.SessionID,
					ExecutionDigest:      request.ExecutionDigest,
					Versions:             request.Versions,
					SourceBuildID:        request.SourceBuildID,
					SourceVersion:        request.SourceVersion,
					VerifyMirrorCommit:   repoClient.VerifyCommit,
					MirrorPollInterval:   5 * time.Second,
					PollInterval:         5 * time.Second,
					TimelinePollInterval: 30 * time.Second,
					PreviousQueueAttempt: request.PreviousQueueAttempt,
					ReconcileAttempts:    6,
					ReconcileInterval:    5 * time.Second,
				}, nil)
			},
		},
	))

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
	fmt.Println("Go-images pipeline 1023 execution is available for normal, rollback, and dev/ test releases.")
	fmt.Printf("Durable session file: %s\n", sessionPath)
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

func validateServeOptions(sessionFile string, enableGoInfraGitHub bool) error {
	if enableGoInfraGitHub && sessionFile == "" {
		return errors.New("-enable-go-infra-github-execution requires -session-file")
	}
	return nil
}

func goImagesModeFromBuild(build *azdopipeline.Build) releasesteps.GoImagesReleaseMode {
	if build == nil {
		return releasesteps.GoImagesReleaseModeNormal
	}
	switch mode := releasesteps.GoImagesReleaseMode(build.Parameters["ReleaseUIGoImagesMode"]); mode {
	case releasesteps.GoImagesReleaseModeNormal,
		releasesteps.GoImagesReleaseModeRollback,
		releasesteps.GoImagesReleaseModeTest:

		return mode
	}
	prefix, _ := build.TemplateParameters["publishRepoPrefix"].(string)
	if prefix == "dev/" {
		return releasesteps.GoImagesReleaseModeTest
	}
	sourceBuild, _ := build.TemplateParameters["sourceBuildPipelineRunId"].(string)
	if sourceBuild != "" && sourceBuild != "$(Build.BuildId)" {
		return releasesteps.GoImagesReleaseModeRollback
	}
	return releasesteps.GoImagesReleaseModeNormal
}
