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

func TestRunFakeGoImagesPipeline(t *testing.T) {
	input := *exampleInput
	input.ReleaseIssue = 42
	var queued bool
	var polled bool
	sb := &ServiceBundleMock{
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
	if len(steps) != 3 {
		t.Fatalf("step count = %d, want 3", len(steps))
	}
	var runner coordinator.StepRunner
	if err := runner.Execute(context.Background(), steps); err != nil {
		t.Fatal(err)
	}
	if !queued || !polled {
		t.Fatalf("queued = %v, polled = %v; want both true", queued, polled)
	}
	if state.Day.GoImagesReleaseBuildID != "build-123" || !state.Day.GoImagesReleaseComplete {
		t.Fatalf("unexpected final state: %#v", state.Day)
	}
	if len(checkpoints) != 2 {
		t.Fatalf("checkpoint count = %d, want 2", len(checkpoints))
	}
	if checkpoints[0].Day.GoImagesReleaseBuildID != "build-123" || checkpoints[0].Day.GoImagesReleaseComplete {
		t.Fatalf("unexpected queue checkpoint: %#v", checkpoints[0].Day)
	}
}

func TestGoImagesReleasePipelineResume(t *testing.T) {
	input := *exampleInput
	state, err := initializeState(&input, nil)
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

func TestRunFakeGoImagesUnofficialDemo(t *testing.T) {
	input := *exampleInput
	state, err := initializeState(&input, nil)
	if err != nil {
		t.Fatal(err)
	}
	state.Day.GoImagesReleaseImported = true
	state.Day.GoImagesReleaseComplete = true
	state.Day.GoImagesReleaseResult = "succeeded"
	state.Day.GoImagesDemoSourceValidated = true
	state.Day.GoImagesReleaseBuildID = "3019035"
	state.Day.GoImagesSourceBranch = "refs/heads/microsoft/main"
	state.Day.GoImagesCommit = "81ce9afc2b75ec4e153dd15fc3c7539b12024945"
	queued := 0
	polled := 0
	service := &ServiceBundleMock{
		TriggerBuildPipelineFunc: func(_ context.Context, pipelineID int, parameters, optionalParameters map[string]string, _ *Secret) (string, error) {
			queued++
			if pipelineID != 1492 {
				t.Fatalf("pipeline ID = %d, want 1492", pipelineID)
			}
			if parameters["publishRepoPrefix"] != "dev/" || parameters["sourceBuildPipelineRunId"] != "$(Build.BuildId)" {
				t.Fatalf("parameters = %#v", parameters)
			}
			if len(optionalParameters) != 0 {
				t.Fatalf("optional parameters = %#v", optionalParameters)
			}
			return "demo-321", nil
		},
		PollPipelineCompleteFunc: func(_ context.Context, buildID string, _ *Secret) error {
			polled++
			if buildID != "demo-321" {
				t.Fatalf("build ID = %q", buildID)
			}
			return nil
		},
	}
	checkpoints := 0
	steps, finalState, err := CreateGoImagesUnofficialDemoGraphWithCheckpoint(
		&input,
		exampleSecret,
		state,
		1492,
		service,
		func(context.Context, *State) error { checkpoints++; return nil },
	)
	if err != nil {
		t.Fatal(err)
	}
	var runner coordinator.StepRunner
	if err := runner.Execute(context.Background(), steps); err != nil {
		t.Fatal(err)
	}
	if queued != 1 || polled != 1 || checkpoints != 3 {
		t.Fatalf("queued = %d, polled = %d, checkpoints = %d", queued, polled, checkpoints)
	}
	if finalState.Day.GoImagesDemoBuildID != "demo-321" || !finalState.Day.GoImagesDemoComplete ||
		finalState.Day.GoImagesDemoParameters["publishRepoPrefix"] != "dev/" {

		t.Fatalf("demo state = %#v", finalState.Day)
	}

	queued = 0
	polled = 0
	steps, _, err = CreateGoImagesUnofficialDemoGraphWithCheckpoint(&input, exampleSecret, finalState, 1492, service, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := runner.Execute(context.Background(), steps); err != nil {
		t.Fatal(err)
	}
	if queued != 0 || polled != 0 {
		t.Fatalf("completed demo was repeated: queued = %d, polled = %d", queued, polled)
	}
}

func TestGoImagesUnofficialDemoRequiresImportedMainRun(t *testing.T) {
	input := *exampleInput
	state, err := initializeState(&input, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := CreateGoImagesUnofficialDemoGraphWithCheckpoint(
		&input, exampleSecret, state, 1492, &ServiceBundleMock{}, nil,
	); err == nil {
		t.Fatal("demo graph accepted state without an imported completed run")
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

	var checkpoints []State
	checkpoint := func(ctx context.Context, state *State) error {
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
	}
	steps, state, err := CreateStepGraphWithCheckpoint(exampleInput, exampleSecret, nil, sb, checkpoint)
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
	if len(checkpoints) == 0 {
		t.Fatal("release completed without recording any state checkpoints")
	}
	if checkpoints[0].Day.ReleaseIssue != releaseIssueNumber {
		t.Fatalf("first checkpoint release issue = %d, want %d", checkpoints[0].Day.ReleaseIssue, releaseIssueNumber)
	}

	// Verify all release state as a golden file. It's intended to be human-readable.
	stateJSON, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	goldentest.Check(t, "fake-complete-release-state.golden.json", string(stateJSON))
	lastCheckpointJSON, err := json.MarshalIndent(checkpoints[len(checkpoints)-1], "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if string(lastCheckpointJSON) != string(stateJSON) {
		t.Fatalf("last checkpoint does not match final release state\ncheckpoint:\n%s\nfinal:\n%s", lastCheckpointJSON, stateJSON)
	}
}

func TestCheckpointFailureStopsRelease(t *testing.T) {
	checkpointErr := errors.New("checkpoint unavailable")
	sb := &ServiceBundleMock{
		CreateReleaseDayTrackingIssueFunc: func(context.Context, string, string, []string, *Secret) (int, error) {
			return 42, nil
		},
	}
	steps, state, err := CreateStepGraphWithCheckpoint(
		&Input{Versions: []string{"1.26.1-1"}},
		&Secret{},
		nil,
		sb,
		func(context.Context, *State) error { return checkpointErr },
	)
	if err != nil {
		t.Fatal(err)
	}
	var runner coordinator.StepRunner
	if err := runner.Execute(context.Background(), steps); !errors.Is(err, checkpointErr) {
		t.Fatalf("Execute error = %v, want checkpoint error", err)
	}
	if state.Day.ReleaseIssue != 42 {
		t.Fatalf("in-memory release issue = %d, want 42", state.Day.ReleaseIssue)
	}
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
