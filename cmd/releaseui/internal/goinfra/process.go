// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package goinfra

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"reflect"
	"strconv"

	releaseui "github.com/microsoft/go-infra/releaseui"
	"github.com/microsoft/go-infra/releaseui/contract"
	"github.com/microsoft/go-infra/releaseui/coordinator"
)

type goInfraProcessPayload struct {
	Input       goInfraPlanInput    `json:"input"`
	PullRequest *GoInfraPullRequest `json:"pullRequest,omitempty"`
}

type releaseOnMergeVariantInput struct {
	PullRequest int
}

type goInfraProcess struct {
	github GitHubService
}

type goInfraRun struct {
	process *goInfraProcess
	state   *contract.State
}

// NewProcess creates the fixed microsoft/go-infra release process.
func NewProcess(github GitHubService) contract.Process {
	return &goInfraProcess{github: github}
}

func (p *goInfraProcess) Definition() contract.Definition {
	releaseOnMergeInputs := newReleaseOnMergeInputSet(new(releaseOnMergeVariantInput)).Inputs()
	return contract.Definition{
		ID: goInfraProcessID, Name: "Go infrastructure", Mark: "IN",
		Description:      "Create the next microsoft/go-infra patch release through its GitHub release workflow.",
		DocumentationURL: "https://github.com/microsoft/go-lab/tree/main/docs/release#microsoftgo-infra",
		Workflow: contract.Workflow{
			Heading: "Choose release path", Description: "Select a release process and review its fixed GitHub action.",
			SubmitLabel: "Review GitHub action",
			Variants: []contract.Variant{
				{
					ID: goInfraActionReleaseOnMerge, Name: "Release on merge",
					Description: "Add release-on-merge to one open, non-fork PR targeting main.",
					Inputs:      releaseOnMergeInputs,
					NoticeTitle: "The UI does not merge the PR.",
					Notice:      "The existing workflow creates the patch release only after the labeled PR is merged.",
				},
				{
					ID: goInfraDispatchModeDryRun, Name: "Patch release dry run",
					Description: "Calculate the next v0.0.x version without creating a release.",
					NoticeTitle: "Dry run does not create a release.",
					Notice:      "The fixed patch-release workflow runs on main with dry-run enabled.",
				},
				{
					ID: goInfraDispatchModePublish, Name: "Publish patch release",
					Description: "Run the workflow on main and create the next patch release.",
					NoticeTitle: "Publishing requires confirmation.",
					Notice:      "The fixed patch-release workflow runs on main and can create the next release.",
				},
			},
		},
	}
}

func (p *goInfraProcess) Preflight(ctx context.Context) (contract.Readiness, error) {
	if err := p.validateConfiguration(); err != nil {
		return contract.Readiness{Details: "External execution is disabled. Restart with the go-infra integration configured."}, nil
	}
	details, err := p.github.Preflight(ctx)
	return contract.Readiness{PlanningEnabled: true, ExecutionEnabled: err == nil, Details: details}, err
}

func (p *goInfraProcess) Prepare(ctx context.Context, selection contract.Selection) (contract.Run, error) {
	if err := p.validateConfiguration(); err != nil {
		return nil, err
	}
	input, err := goInfraSelectionInput(selection)
	if err != nil {
		return nil, contract.InvalidInput(err)
	}
	prepared, err := prepareGoInfraProcess(ctx, selection.VariantID, input, p.github)
	if err != nil {
		return nil, err
	}
	state, err := releaseui.NewReleaseRunState(goInfraProcessID, prepared)
	if err != nil {
		return nil, fmt.Errorf("create go-infra release run: %w", err)
	}
	return p.Restore(state)
}

func (p *goInfraProcess) Restore(state *contract.State) (contract.Run, error) {
	if err := validateGoInfraProcessRun(state); err != nil {
		return nil, err
	}
	return &goInfraRun{process: p, state: releaseui.CloneReleaseRunState(state)}, nil
}

func (r *goInfraRun) Snapshot() *contract.State {
	return releaseui.CloneReleaseRunState(r.state)
}

func (r *goInfraRun) Steps(
	_ context.Context,
	checkpoint contract.CheckpointFunc,
) ([]*coordinator.Step, error) {
	if err := r.process.validateConfiguration(); err != nil {
		return nil, err
	}
	run := r.Snapshot()
	if err := validateGoInfraProcessRun(run); err != nil {
		return nil, err
	}
	payload, err := decodeStrictJSON[goInfraProcessPayload](run.Payload)
	if err != nil {
		return nil, err
	}
	payloadJSON := append(json.RawMessage(nil), run.Payload...)
	state := append(json.RawMessage(nil), run.Checkpoint...)
	action := func(ctx context.Context) error {
		if len(state) > 0 {
			return resumeGoInfraProcess(ctx, payloadJSON, state, checkpoint, r.process.github)
		}
		return executeGoInfraProcess(ctx, payloadJSON, checkpoint, r.process.github)
	}
	return []*coordinator.Step{goInfraProcessStep(payload, action)}, nil
}

func (p *goInfraProcess) validateConfiguration() error {
	if p.github == nil {
		return errors.New("go-infra integration is incomplete")
	}
	return nil
}

func prepareGoInfraProcess(
	ctx context.Context,
	variantID string,
	input goInfraPlanInput,
	github GitHubService,
) (contract.Plan, error) {
	normalized, pullRequestNumber, err := normalizeGoInfraPlanInput(input)
	if err != nil {
		return contract.Plan{}, contract.InvalidInput(err)
	}
	payload := goInfraProcessPayload{Input: normalized}
	if normalized.Action == goInfraActionReleaseOnMerge {
		pullRequest, err := github.GetPullRequest(ctx, pullRequestNumber)
		if err != nil {
			return contract.Plan{}, fmt.Errorf("validate go-infra pull request: %w", err)
		}
		if err := validateGoInfraPullRequest(pullRequest, pullRequestNumber); err != nil {
			return contract.Plan{}, err
		}
		payload.PullRequest = &pullRequest
	}
	normalizedJSON, err := json.Marshal(normalized)
	if err != nil {
		return contract.Plan{}, fmt.Errorf("encode normalized go-infra input: %w", err)
	}
	payloadJSON, err := json.Marshal(payload)
	if err != nil {
		return contract.Plan{}, fmt.Errorf("encode go-infra process plan: %w", err)
	}
	return contract.Plan{
		VariantID: variantID,
		Test:      goInfraProcessIsTest(normalized), Input: normalizedJSON, Payload: payloadJSON,
		View: goInfraProcessView(payload), Target: goInfraProcessTarget(payload),
	}, nil
}

func newReleaseOnMergeInputSet(input *releaseOnMergeVariantInput) *contract.InputSet {
	inputs := contract.NewInputSet()
	inputs.PositiveIntVar(&input.PullRequest, "pullRequest", contract.FieldOptions{
		Label: "Pull request number", Placeholder: "123",
		Description: "The server verifies that the PR is open, targets main, and does not come from a fork.",
	})
	return inputs
}

func goInfraSelectionInput(selection contract.Selection) (goInfraPlanInput, error) {
	inputs := contract.NewInputSet()
	switch selection.VariantID {
	case goInfraActionReleaseOnMerge:
		var releaseOnMerge releaseOnMergeVariantInput
		inputs = newReleaseOnMergeInputSet(&releaseOnMerge)
		if err := inputs.Parse(selection.Input); err != nil {
			return goInfraPlanInput{}, err
		}
		return goInfraPlanInput{
			Action: goInfraActionReleaseOnMerge, PullRequest: strconv.Itoa(releaseOnMerge.PullRequest),
		}, nil
	case goInfraDispatchModeDryRun, goInfraDispatchModePublish:
		if err := inputs.Parse(selection.Input); err != nil {
			return goInfraPlanInput{}, err
		}
		return goInfraPlanInput{
			Action: goInfraActionManualDispatch, DispatchMode: selection.VariantID,
		}, nil
	default:
		return goInfraPlanInput{}, fmt.Errorf("unsupported go-infra release variant %q", selection.VariantID)
	}
}

func executeGoInfraProcess(
	ctx context.Context,
	payloadJSON json.RawMessage,
	checkpoint contract.CheckpointFunc,
	github GitHubService,
) error {
	payload, err := decodeStrictJSON[goInfraProcessPayload](payloadJSON)
	if err != nil {
		return err
	}
	switch payload.Input.Action {
	case goInfraActionReleaseOnMerge:
		_, err := github.AddReleaseOnMergeLabel(ctx, payload.PullRequest.Number, payload.PullRequest.HeadSHA)
		return err
	case goInfraActionManualDispatch:
		run, err := github.DispatchPatchRelease(ctx, payload.Input.DispatchMode == goInfraDispatchModeDryRun)
		if err != nil {
			return err
		}
		if err := checkpointGoInfraProcess(ctx, run, checkpoint); err != nil {
			return err
		}
		return pollGoInfraProcess(ctx, run.ID, checkpoint, github)
	default:
		return fmt.Errorf("unsupported go-infra action %q", payload.Input.Action)
	}
}

func resumeGoInfraProcess(
	ctx context.Context,
	payloadJSON, state json.RawMessage,
	checkpoint contract.CheckpointFunc,
	github GitHubService,
) error {
	payload, err := decodeStrictJSON[goInfraProcessPayload](payloadJSON)
	if err != nil {
		return err
	}
	if payload.Input.Action != goInfraActionManualDispatch {
		return fmt.Errorf("go-infra action %q cannot resume from a workflow run", payload.Input.Action)
	}
	run, err := decodeStrictJSON[GoInfraWorkflowRun](state)
	if err != nil {
		return err
	}
	if err := validateGoInfraWorkflowRun(&run); err != nil {
		return err
	}
	return pollGoInfraProcess(ctx, run.ID, checkpoint, github)
}

func pollGoInfraProcess(
	ctx context.Context,
	runID int64,
	checkpoint contract.CheckpointFunc,
	github GitHubService,
) error {
	_, err := github.PollWorkflowRun(ctx, runID, func(run GoInfraWorkflowRun) error {
		return checkpointGoInfraProcess(ctx, run, checkpoint)
	})
	return err
}

func checkpointGoInfraProcess(ctx context.Context, run GoInfraWorkflowRun, checkpoint contract.CheckpointFunc) error {
	if err := validateGoInfraWorkflowRun(&run); err != nil {
		return err
	}
	state, err := json.Marshal(run)
	if err != nil {
		return fmt.Errorf("encode go-infra workflow run: %w", err)
	}
	return checkpoint(ctx, contract.Checkpoint{
		State: state, External: processRunReference(goInfraExternalRun(run)), Progress: goInfraProcessProgress(run),
	})
}

func processRunReference(reference contract.Reference) *contract.Reference {
	return &reference
}

func validateGoInfraProcessRun(run *contract.State) error {
	if run == nil || run.ProcessID != goInfraProcessID {
		return errors.New("go-infra process run has an invalid process ID")
	}
	payload, err := decodeStrictJSON[goInfraProcessPayload](run.Payload)
	if err != nil {
		return fmt.Errorf("decode go-infra process payload: %w", err)
	}
	input, err := decodeStrictJSON[goInfraPlanInput](run.Input)
	if err != nil {
		return fmt.Errorf("decode go-infra process input: %w", err)
	}
	normalized, pullRequestNumber, err := normalizeGoInfraPlanInput(payload.Input)
	if err != nil || normalized != payload.Input || input != payload.Input {
		return errors.New("go-infra process input is invalid")
	}
	if run.Test != goInfraProcessIsTest(payload.Input) {
		return errors.New("go-infra process test classification is invalid")
	}
	if run.VariantID != goInfraVariantID(payload.Input) {
		return errors.New("go-infra process variant does not match its input")
	}
	switch payload.Input.Action {
	case goInfraActionReleaseOnMerge:
		if payload.PullRequest == nil {
			return errors.New("release-on-merge plan has no pull request")
		}
		if err := validateGoInfraPullRequest(*payload.PullRequest, pullRequestNumber); err != nil {
			return err
		}
		if len(run.Checkpoint) > 0 || run.External != nil {
			return errors.New("release-on-merge plan contains a workflow run")
		}
	case goInfraActionManualDispatch:
		if payload.PullRequest != nil {
			return errors.New("manual-dispatch plan contains a pull request")
		}
		if run.Complete && run.Result != "uncertain" && len(run.Checkpoint) == 0 {
			return errors.New("completed manual-dispatch plan has no workflow run")
		}
	default:
		return fmt.Errorf("unsupported go-infra action %q", payload.Input.Action)
	}
	if run.Target != goInfraProcessTarget(payload) ||
		!reflect.DeepEqual(run.View, goInfraProcessView(payload)) {

		return errors.New("go-infra process plan does not match its fixed policy")
	}
	if len(run.Checkpoint) > 0 {
		workflowRun, err := decodeStrictJSON[GoInfraWorkflowRun](run.Checkpoint)
		if err != nil {
			return fmt.Errorf("decode go-infra workflow checkpoint: %w", err)
		}
		if err := validateGoInfraWorkflowRun(&workflowRun); err != nil {
			return err
		}
		if run.External == nil || *run.External != goInfraExternalRun(workflowRun) {
			return errors.New("go-infra workflow checkpoint does not match its external run")
		}
	}
	return nil
}

func goInfraProcessIsTest(input goInfraPlanInput) bool {
	return input.Action == goInfraActionManualDispatch && input.DispatchMode == goInfraDispatchModeDryRun
}

func goInfraVariantID(input goInfraPlanInput) string {
	if input.Action == goInfraActionManualDispatch {
		return input.DispatchMode
	}
	return input.Action
}

func goInfraProcessStep(payload goInfraProcessPayload, action func(context.Context) error) *coordinator.Step {
	name := "Apply release-on-merge label"
	timeout := goInfraLabelTimeout
	if payload.Input.Action == goInfraActionManualDispatch {
		timeout = goInfraWorkflowTimeout
		if payload.Input.DispatchMode == goInfraDispatchModeDryRun {
			name = "Dispatch patch-release dry run"
		} else {
			name = "Dispatch patch release"
		}
	}
	return coordinator.NewRootStep(name, timeout, action)
}

func goInfraProcessTarget(payload goInfraProcessPayload) contract.Reference {
	if payload.Input.Action == goInfraActionReleaseOnMerge {
		return contract.Reference{
			ID: "pr-" + payload.Input.PullRequest, URL: payload.PullRequest.URL,
			LinkLabel: "Open go-infra PR #" + payload.Input.PullRequest,
		}
	}
	return contract.Reference{
		ID: payload.Input.DispatchMode, URL: goInfraWorkflowURL, LinkLabel: "Open go-infra workflow runs",
	}
}

func goInfraExternalRun(run GoInfraWorkflowRun) contract.Reference {
	return contract.Reference{
		ID: strconv.FormatInt(run.ID, 10), URL: run.URL,
		LinkLabel: "Open go-infra workflow run " + strconv.FormatInt(run.ID, 10),
		Status:    run.Status, Terminal: run.Status == "completed", Succeeded: run.Conclusion == "success",
	}
}

func goInfraProcessProgress(run GoInfraWorkflowRun) contract.Progress {
	progress := contract.Progress{
		Summary: "GitHub workflow is queued",
		Detail:  fmt.Sprintf("Waiting for run %d to start", run.ID),
	}
	switch run.Status {
	case "in_progress":
		progress.Summary = "GitHub workflow is running"
		progress.Detail = fmt.Sprintf("Run %d is in progress", run.ID)
	case "completed":
		progress.Summary = "GitHub workflow completed"
		progress.Detail = fmt.Sprintf("Run %d completed with conclusion %s", run.ID, run.Conclusion)
		progress.Completed = 1
		progress.Total = 1
	}
	return progress
}

func goInfraProcessView(payload goInfraProcessPayload) contract.PlanView {
	view := contract.PlanView{
		Subtitle: "GitHub action · 1 step",
		Request: &contract.RequestPreview{
			Eyebrow: "GitHub request preview · not sent",
			Target:  goInfraRepository,
		},
	}
	switch payload.Input.Action {
	case goInfraActionReleaseOnMerge:
		pullRequest := payload.PullRequest
		view.IntentTitle = fmt.Sprintf("Apply %s to PR #%d", goInfraReleaseLabel, pullRequest.Number)
		view.IntentBadge = goInfraReleaseLabel
		view.Facts = []contract.PlanFact{
			{Label: "Pull request", Value: fmt.Sprintf("#%d · %s", pullRequest.Number, pullRequest.Title), Href: pullRequest.URL},
			{Label: "Target", Value: pullRequest.BaseRef, Detail: pullRequest.HeadRef + " @ " + pullRequest.HeadSHA},
		}
		view.Request.Title = "Add pull request label"
		view.Request.Fields = []contract.RequestField{
			{Name: "pullRequest", Value: strconv.Itoa(pullRequest.Number)},
			{Name: "expectedHeadSHA", Value: pullRequest.HeadSHA},
			{Name: "label", Value: goInfraReleaseLabel},
		}
		view.ExecutionTitle = "Apply release-on-merge label"
		view.ExecutionWarning = "This adds release-on-merge to the reviewed non-fork PR. It does not merge the PR. Merging it later causes the existing GitHub workflow to create the patch release."
		view.ExecutionConfirmation = fmt.Sprintf("Confirm adding %s to go-infra PR #%d at %s.", goInfraReleaseLabel, pullRequest.Number, pullRequest.HeadSHA)
		view.ExecutionButtonLabel = "Apply release-on-merge label"
	case goInfraActionManualDispatch:
		dryRun := payload.Input.DispatchMode == goInfraDispatchModeDryRun
		mode := "Publish"
		if dryRun {
			mode = "Dry run"
		}
		view.IntentTitle = mode + " the next go-infra patch release"
		view.IntentBadge = payload.Input.DispatchMode
		view.Facts = []contract.PlanFact{{Label: "Workflow", Value: goInfraWorkflowFile, Href: goInfraWorkflowURL}, {Label: "Ref", Value: goInfraDefaultRef}}
		view.Request.Title = "Dispatch GitHub Actions workflow"
		view.Request.Fields = []contract.RequestField{{Name: "workflow", Value: goInfraWorkflowFile}, {Name: "ref", Value: goInfraDefaultRef}, {Name: "dry-run", Value: strconv.FormatBool(dryRun)}}
		if dryRun {
			view.ExecutionTitle = "Run release dry run"
			view.ExecutionWarning = "This dispatches the existing workflow with dry-run=true. It calculates the next version but does not create a release."
			view.ExecutionConfirmation = "Confirm dispatching the go-infra patch-release workflow on main in dry-run mode."
			view.ExecutionButtonLabel = "Run dry run"
		} else {
			view.ExecutionTitle = "Publish patch release"
			view.ExecutionWarning = "This immediately dispatches the existing workflow on main with dry-run=false. The workflow can create the next go-infra patch release."
			view.ExecutionConfirmation = "Confirm dispatching the go-infra patch-release workflow on main to publish a release."
			view.ExecutionButtonLabel = "Publish patch release"
		}
	}
	return view
}

func decodeStrictJSON[T any](data json.RawMessage) (T, error) {
	var value T
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&value); err != nil {
		return value, fmt.Errorf("decode process data: %w", err)
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return value, errors.New("process data must contain exactly one JSON value")
	}
	return value, nil
}

var (
	_ contract.Process = (*goInfraProcess)(nil)
	_ contract.Run     = (*goInfraRun)(nil)
)
