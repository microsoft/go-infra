// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package releasesteps

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/microsoft/go-infra/cmd/releaseagent/internal/coordinator"
	"github.com/microsoft/go-infra/goldentest"
)

var exampleInput = &Input{
	Versions:                   []string{"1.22.10-1", "1.23.4-1"},
	GoImagesReleaseMode:        GoImagesReleaseModeNormal,
	GoImagesSourceBranch:       "refs/heads/microsoft/main",
	GoImagesSourceVersion:      "81ce9afc2b75ec4e153dd15fc3c7539b12024945",
	Security:                   false,
	RunnerGitHubUser:           "ghost",
	ReleaseConfigVariableGroup: "go-release-variables",

	TargetRepo:     "microsoft/go",
	TargetAzDORepo: "dnceng/internal/_git/microsoft-go", // Implied https://dev.azure.com/

	TargetGoImagesRepo:     "microsoft/go-images",
	TargetAzDOGoImagesRepo: "dnceng/internal/_git/microsoft-go-images",

	MicrosoftGoPipeline:          20,
	MicrosoftGoInnerloopPipeline: 30,
	MicrosoftGoImagesPipeline:    1023,
	MicrosoftGoAkaMSPipeline:     50,
	AzureLinuxCreatePRPipeline:   60,
}

var exampleGoImagesInput = &GoImagesInput{
	Versions:      []string{"1.22.10-1", "1.23.4-1"},
	Mode:          GoImagesReleaseModeNormal,
	SourceBranch:  "refs/heads/microsoft/main",
	SourceVersion: "81ce9afc2b75ec4e153dd15fc3c7539b12024945",
	MirrorTarget:  GoImagesInternalMirrorTarget,
	PipelineID:    1023,
}

func TestGoImagesPipelineParameters(t *testing.T) {
	parameters := GoImagesPipelineParameters()
	want := map[string]string{
		"_info":                    "🔵  go-docker-rolling-internal-pipeline.yml  🔵 🔵",
		"sourceBuildPipelineRunId": "$(Build.BuildId)",
		"publishRepoPrefix":        "public/",
	}
	if actual, expected := mustJSON(t, parameters), mustJSON(t, want); actual != expected {
		t.Fatalf("parameters mismatch\nactual: %s\nwant:   %s", actual, expected)
	}
}

func TestGoImagesPipelineParametersForMode(t *testing.T) {
	for _, test := range []struct {
		mode          GoImagesReleaseMode
		sourceBuildID string
		wantSource    string
		wantPrefix    string
	}{
		{mode: GoImagesReleaseModeNormal, wantSource: "$(Build.BuildId)", wantPrefix: "public/"},
		{mode: GoImagesReleaseModeRollback, sourceBuildID: "3019035", wantSource: "3019035", wantPrefix: "public/"},
		{mode: GoImagesReleaseModeTest, wantSource: "$(Build.BuildId)", wantPrefix: "dev/"},
	} {
		t.Run(string(test.mode), func(t *testing.T) {
			parameters, err := GoImagesPipelineParametersForMode(test.mode, test.sourceBuildID)
			if err != nil {
				t.Fatal(err)
			}
			if parameters["sourceBuildPipelineRunId"] != test.wantSource || parameters["publishRepoPrefix"] != test.wantPrefix {
				t.Fatalf("parameters = %#v", parameters)
			}
		})
	}
	if _, err := GoImagesPipelineParametersForMode(GoImagesReleaseModeNormal, "123"); err == nil {
		t.Fatal("normal release accepted a source build")
	}
	if _, err := GoImagesPipelineParametersForMode(GoImagesReleaseModeRollback, "not-a-build"); err == nil {
		t.Fatal("rollback accepted an invalid source build")
	}
}

func TestRunFakeGoImagesPipeline(t *testing.T) {
	input := *exampleGoImagesInput
	var mirrorVerified bool
	var queued bool
	var polled bool
	sb := &ServiceBundleMock{
		PollAzDOMirrorFunc: func(_ context.Context, target, commit string, _ *Secret) error {
			mirrorVerified = true
			if target != input.MirrorTarget || commit != input.SourceVersion {
				t.Fatalf("mirror target = %q at %q, want %q at %q", target, commit, input.MirrorTarget, input.SourceVersion)
			}
			return nil
		},
		TriggerBuildPipelineFunc: func(_ context.Context, pipelineID int, parameters, optionalParameters map[string]string, _ *Secret) (string, error) {
			queued = true
			if pipelineID != 1023 {
				t.Fatalf("pipeline ID = %d, want 1023", pipelineID)
			}
			if parameters["sourceBuildPipelineRunId"] != "$(Build.BuildId)" || parameters["publishRepoPrefix"] != "public/" {
				t.Fatalf("unexpected direct pipeline parameters: %#v", parameters)
			}
			if optionalParameters != nil {
				t.Fatalf("optional parameters = %#v, want nil", optionalParameters)
			}
			return "build-123", nil
		},
		PollPipelineCompleteFunc: func(_ context.Context, buildID string, _ *Secret) error {
			polled = true
			if buildID != "build-123" {
				t.Fatalf("build ID = %q, want build-123", buildID)
			}
			return nil
		},
	}
	var checkpoints []State
	steps, state, err := CreateGoImagesPipelineGraphWithCheckpoint(
		&input,
		exampleSecret,
		nil,
		sb,
		func(_ context.Context, state *State) error {
			data, err := json.Marshal(state)
			if err != nil {
				return err
			}
			var clone State
			if err := json.Unmarshal(data, &clone); err != nil {
				return err
			}
			checkpoints = append(checkpoints, clone)
			return nil
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	if len(steps) != 4 {
		t.Fatalf("step count = %d, want 4", len(steps))
	}
	var runner coordinator.StepRunner
	if err := runner.Execute(context.Background(), steps); err != nil {
		t.Fatal(err)
	}
	if !mirrorVerified || !queued || !polled {
		t.Fatalf("mirror verified = %v, queued = %v, polled = %v; want all true", mirrorVerified, queued, polled)
	}
	if state.Day.GoImagesReleaseBuildID != "build-123" || !state.Day.GoImagesReleaseComplete {
		t.Fatalf("unexpected final state: %#v", state.Day)
	}
	if len(checkpoints) != 3 {
		t.Fatalf("checkpoint count = %d, want 3", len(checkpoints))
	}
	if !checkpoints[0].Day.GoImagesReleaseQueueAttempted || checkpoints[0].Day.GoImagesReleaseBuildID != "" {
		t.Fatalf("unexpected pre-queue checkpoint: %#v", checkpoints[0].Day)
	}
	if checkpoints[1].Day.GoImagesReleaseBuildID != "build-123" || checkpoints[1].Day.GoImagesReleaseComplete {
		t.Fatalf("unexpected queue checkpoint: %#v", checkpoints[1].Day)
	}
}

func TestGoImagesReleaseBlocksQueueUntilCommitIsMirrored(t *testing.T) {
	input := *exampleGoImagesInput
	mirrorErr := errors.New("commit is not mirrored")
	queued := false
	sb := &ServiceBundleMock{
		PollAzDOMirrorFunc: func(_ context.Context, target, commit string, _ *Secret) error {
			return mirrorErr
		},
		TriggerBuildPipelineFunc: func(context.Context, int, map[string]string, map[string]string, *Secret) (string, error) {
			queued = true
			return "", nil
		},
		PollPipelineCompleteFunc: func(context.Context, string, *Secret) error {
			return nil
		},
	}
	steps, _, err := CreateGoImagesPipelineGraph(&input, exampleSecret, nil, sb)
	if err != nil {
		t.Fatal(err)
	}
	var runner coordinator.StepRunner
	if err := runner.Execute(context.Background(), steps); !errors.Is(err, mirrorErr) {
		t.Fatalf("error = %v, want %v", err, mirrorErr)
	}
	if queued {
		t.Fatal("pipeline was queued before the source commit was mirrored")
	}
}

func TestGoImagesReleasePipelineResume(t *testing.T) {
	input := *exampleGoImagesInput
	state, err := initializeState(goImagesCompatibilityInput(&input), nil)
	if err != nil {
		t.Fatal(err)
	}
	state.Day.GoImagesReleaseBuildID = "existing-build"
	sb := &ServiceBundleMock{
		TriggerBuildPipelineFunc: func(context.Context, int, map[string]string, map[string]string, *Secret) (string, error) {
			t.Fatal("pipeline was queued again")
			return "", nil
		},
		PollPipelineCompleteFunc: func(_ context.Context, buildID string, _ *Secret) error {
			if buildID != "existing-build" {
				t.Fatalf("build ID = %q, want existing-build", buildID)
			}
			return nil
		},
	}
	steps, state, err := CreateGoImagesPipelineGraph(&input, exampleSecret, state, sb)
	if err != nil {
		t.Fatal(err)
	}
	var runner coordinator.StepRunner
	if err := runner.Execute(context.Background(), steps); err != nil {
		t.Fatal(err)
	}
	if !state.Day.GoImagesReleaseComplete {
		t.Fatal("restored pipeline was not marked complete")
	}
}

func mustJSON(t *testing.T, value any) string {
	t.Helper()
	data, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
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
	goldentest.Check(t, "step-graph.golden.md", goldenMermaid)

	stateJSON, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	goldentest.Check(t, "default-state.golden.json", string(stateJSON))
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
	for _, call := range sb.CreateDockerImagesPRCalls() {
		if call.Repo != exampleInput.TargetGoImagesRepo {
			t.Errorf("CreateDockerImagesPR repo = %q, want %q", call.Repo, exampleInput.TargetGoImagesRepo)
		}
	}
	for _, call := range sb.PollMergedGitHubPRCommitCalls() {
		if call.Pr == 50 && call.Repo != exampleInput.TargetGoImagesRepo {
			t.Errorf("image PR merge poll repo = %q, want %q", call.Repo, exampleInput.TargetGoImagesRepo)
		}
	}
	// Verify all release state as a golden file. It's intended to be human-readable.
	stateJSON, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	goldentest.Check(t, "fake-complete-release-state.golden.json", string(stateJSON))
}

func TestStateAccessRetriesDirtyCheckpoint(t *testing.T) {
	checkpointErr := errors.New("checkpoint unavailable")
	fail := true
	checkpointCalls := 0
	state := &State{}
	access := &stateAccess{
		state: state,
		checkpoint: func(context.Context, *State) error {
			checkpointCalls++
			if fail {
				return checkpointErr
			}
			return nil
		},
	}
	if err := access.update(context.Background(), func(state *State) {
		state.Day.ReleaseIssue = 42
	}); !errors.Is(err, checkpointErr) {
		t.Fatalf("update error = %v, want checkpoint error", err)
	}
	fail = false
	if err := access.flush(context.Background()); err != nil {
		t.Fatalf("flush failed: %v", err)
	}
	if checkpointCalls != 2 {
		t.Fatalf("checkpoint calls = %d, want 2", checkpointCalls)
	}
	if state.Day.ReleaseIssue != 42 {
		t.Fatalf("release issue = %d, want 42", state.Day.ReleaseIssue)
	}
}
