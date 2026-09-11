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
	"github.com/microsoft/go-infra/cmd/releaseagent/internal/azdoworkitem"
	"github.com/microsoft/go-infra/cmd/releaseagent/internal/goimagesexecution"
	"github.com/microsoft/go-infra/cmd/releaseagent/internal/goimagesrelease"
	"github.com/microsoft/go-infra/cmd/releaseagent/internal/goimagesworkflow"
	"github.com/microsoft/go-infra/cmd/releaseagent/internal/goinfragithub"
	"github.com/microsoft/go-infra/cmd/releaseagent/internal/releaseui"
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

	service, err := goinfragithub.New(goinfragithub.ExecCommandRunner{})
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
	goImagesStore, err := releaseui.NewGoImagesWorkItemStore(workItems, assignedTo)
	if err != nil {
		return err
	}
	processRunStore, err := releaseui.NewProcessRunWorkItemStore(workItems, assignedTo)
	if err != nil {
		return err
	}
	options := []releaseui.Option{
		releaseui.WithReleaseWorkItems(workItems),
		releaseui.WithSessionStore(goImagesStore),
		releaseui.WithProcessRunStore(processRunStore),
		releaseui.WithGoInfraGitHubIntegration(releaseui.GoInfraGitHubIntegration{
			Preflight: service.Preflight, GetPullRequest: service.GetPullRequest,
			AddReleaseOnMergeLabel: service.AddReleaseOnMergeLabel,
			DispatchPatchRelease:   service.DispatchPatchRelease, PollWorkflowRun: service.PollWorkflowRun,
		}),
	}
	if *releaseWorkItem > 0 {
		selected, err := workItems.Get(context.Background(), *releaseWorkItem)
		if err != nil {
			return fmt.Errorf("select release work item %d: %w", *releaseWorkItem, err)
		}
		if selected.Snapshot.ProcessID == "go-images" {
			options = append(options, releaseui.WithGoImagesWorkItem(*releaseWorkItem))
		} else {
			options = append(options, releaseui.WithProcessRunWorkItem(*releaseWorkItem))
		}
	}

	azureHTTPClient := &http.Client{Timeout: 3 * time.Minute}
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
		Preflight: func(ctx context.Context) (string, error) {
			definition, err := azureClient.GetDefinition(ctx, goimagesworkflow.DefinitionID)
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
			return "Authenticated and verified direct go-images pipeline 1023. Planning is read-only; confirmed execution can queue only this target.", nil
		},
		ResolveCurrentSource: resolveCurrentSource,
		ValidateRollback: func(ctx context.Context, buildID int) (releaseui.GoImagesRollbackSource, error) {
			source, err := goimagesrelease.ValidateRollbackSource(ctx, azureClient, versionResolver, buildID)
			if err != nil {
				return releaseui.GoImagesRollbackSource{}, err
			}
			return releaseui.GoImagesRollbackSource{
				BuildID: source.BuildID, URL: source.URL, Versions: source.Versions,
			}, nil
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
			NewService: func(request releaseui.GoImagesExecutionRequest) (goimagesworkflow.Service, error) {
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
	if *releaseWorkItem > 0 {
		fmt.Printf("Restored Azure DevOps work item: %d\n", *releaseWorkItem)
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
