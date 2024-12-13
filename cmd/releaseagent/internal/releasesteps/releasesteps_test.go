// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package releasesteps

import (
	"context"
	"encoding/json"
	"path/filepath"
	"testing"

	"github.com/microsoft/go-infra/cmd/releaseagent/internal/coordinator"
	"github.com/microsoft/go-infra/goldentest"
)

var exampleInput = &Input{
	Versions:                   []string{"1.22.10-1", "1.23.4-1"},
	Security:                   false,
	RunnerGitHubUser:           "ghost",
	ReleaseConfigVariableGroup: "go-release-variables",

	TargetRepo:     "microsoft/go",
	TargetAzDORepo: "dnceng/internal/_git/microsoft-go", // Implied https://dev.azure.com/

	TargetGoImagesRepo:     "microsoft/go-images",
	TargetAzDOGoImagesRepo: "dnceng/internal/_git/microsoft-go-images",

	MicrosoftGoPipeline:          20,
	MicrosoftGoInnerloopPipeline: 30,
	MicrosoftGoImagesPipeline:    40,
	MicrosoftGoAkaMSPipeline:     50,
	AzureLinuxCreatePRPipeline:   60,
}

var exampleSecret = &Secret{
	GitHubPAT:         "Placeholder",
	GitHubReviewerPAT: "Placeholder" + "Reviewer",
	AzDOPAT:           "Placeholder" + "AzDO",
}

func TestCreateStepGraphGolden(t *testing.T) {
	steps, state, err := CreateStepGraph(
		exampleInput,
		exampleSecret,
		nil, // We want to see what the func will generate as the default state.
		nil,
	)
	if err != nil {
		t.Fatal(err)
	}

	src := coordinator.CreateMermaidStepFlowchart(steps)

	url, err := coordinator.MermaidLiveChartURL(src, false)
	if err != nil {
		t.Fatal(err)
	}

	// Add a live editor link to help visualize easily.
	goldenMermaid := src + "\n%% " + url + "\n"

	// Use "md" file: help highlight the link for devs, and there is no clear better extension.
	goldentest.Check(t, filepath.Join("testdata", "step-graph.golden.md"), goldenMermaid)

	stateJSON, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	goldentest.Check(t, filepath.Join("testdata", "default-state.golden.json"), string(stateJSON))
}

func TestRunFakeRelease(t *testing.T) {
	// Run a fake release with mock services that should complete essentially immediately.
	// Essentially a bare minimum exercise of the series of steps.
	// Also, can potentially detect bad variable use in the parallel steps with "go test -race".
	releaseIssueNumber := 42
	sb := &ServiceBundleMock{
		CreateReleaseDayTrackingIssueFunc: func(ctx context.Context, repo, runner string, versions []string, secret *Secret) (int, error) {
			return releaseIssueNumber, nil
		},
		PollUpstreamTagCommitFunc: func(ctx context.Context, version string) (string, error) {
			return "abcdef-upstream-commit", nil
		},
		CreateGitHubSyncPRFunc: func(ctx context.Context, repo, branch string, secret *Secret) (int, error) {
			return 1234, nil
		},
		PollMergedGitHubPRCommitFunc: func(ctx context.Context, repo string, pr int, secret *Secret) (string, error) {
			return "abcdef-merged-commit", nil
		},
		PollAzDOMirrorFunc: func(ctx context.Context, target, commit string, secret *Secret) error {
			return nil
		},
		GetTargetBranchFunc: func(ctx context.Context, version string) (string, error) {
			return "target-branch-" + version, nil
		},
		TriggerBuildPipelineFunc: func(ctx context.Context, pipelineID int, parameters, optionalParameters map[string]string, secret *Secret) (string, error) {
			return "12345-running-pipeline", nil
		},
		PollPipelineCompleteFunc: func(ctx context.Context, buildID string, secret *Secret) error {
			return nil
		},
		DownloadPipelineArtifactToDirFunc: func(ctx context.Context, buildID, artifactName string, secret *Secret) (string, error) {
			return `C:\tmp\go-artifacts\location\` + artifactName, nil
		},
		VerifyAssetVersionFunc: func(ctx context.Context, assetJSONPath string, version string) error {
			return nil
		},
		CreateGitHubTagFunc: func(ctx context.Context, version, repo, tag, commit string, secret *Secret) error {
			return nil
		},
		CreateGitHubReleaseFunc: func(ctx context.Context, repo, tag, assetJSONPath, buildAssetDir string, secret *Secret) error {
			return nil
		},
		CreateDockerImagesPRFunc: func(ctx context.Context, repo, assetJSONPath, manualBranch string, secret *Secret) (int, error) {
			return 50, nil
		},
		PollImagesCommitFunc: func(ctx context.Context, versions []string, secret *Secret) (string, error) {
			return "abcdef-images-with-versions", nil
		},
		CheckLatestMARGoVersionFunc: func(ctx context.Context, versions []string) error {
			return nil
		},
		CreateAnnouncementBlogFileFunc: func(ctx context.Context, versions []string, user string, security bool, secret *Secret) error {
			return nil
		},
	}

	steps, state, err := CreateStepGraph(exampleInput, exampleSecret, nil, sb)
	if err != nil {
		t.Fatal(err)
	}
	var runner coordinator.StepRunner
	if err := runner.Execute(context.Background(), steps); err != nil {
		t.Fatal(err)
	}

	// Verify some self-consistent calls. Not exhaustive, rather a spot check of a few basics,
	// areas that seem risky, and regression checks. Put a debug breakpoint here to explore the
	// mock call records.
	if len(sb.CreateReleaseDayTrackingIssueCalls()) != 1 {
		t.Errorf("expected 1 CreateReleaseDayTrackingIssue call, got %d", len(sb.CreateReleaseDayTrackingIssueCalls()))
	} else {
		if sb.CreateReleaseDayTrackingIssueCalls()[0].Versions[0] != "1.22.10-1" {
			t.Errorf("expected version 1.22.10-1, got %s", sb.CreateReleaseDayTrackingIssueCalls()[0].Versions[0])
		}
	}

	// Verify all release state as a golden file. It's intended to be human-readable.
	stateJSON, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	goldentest.Check(t, filepath.Join("testdata", "fake-complete-release-state.golden.json"), string(stateJSON))
}
