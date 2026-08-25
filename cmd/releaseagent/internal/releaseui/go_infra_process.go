// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package releaseui

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"reflect"
	"strconv"
)

type goInfraProcessPayload struct {
	Input       goInfraPlanInput    `json:"input"`
	PullRequest *GoInfraPullRequest `json:"pullRequest,omitempty"`
}

func newGoInfraProcessExecutor(integration GoInfraGitHubIntegration) ProcessExecutor {
	if integration.Preflight == nil || integration.GetPullRequest == nil || integration.AddReleaseOnMergeLabel == nil ||
		integration.DispatchPatchRelease == nil || integration.PollWorkflowRun == nil {

		return ProcessExecutor{}
	}
	return ProcessExecutor{
		Preflight: integration.Preflight,
		Prepare: func(ctx context.Context, input json.RawMessage) (ProcessPreparedRun, error) {
			return prepareGoInfraProcess(ctx, input, integration)
		},
		Execute: func(ctx context.Context, payload json.RawMessage, checkpoint ProcessCheckpointFunc) error {
			return executeGoInfraProcess(ctx, payload, checkpoint, integration)
		},
		Resume: func(ctx context.Context, payload, state json.RawMessage, checkpoint ProcessCheckpointFunc) error {
			return resumeGoInfraProcess(ctx, payload, state, checkpoint, integration)
		},
		Validate: validateGoInfraProcessRun,
	}
}

func prepareGoInfraProcess(
	ctx context.Context,
	inputJSON json.RawMessage,
	integration GoInfraGitHubIntegration,
) (ProcessPreparedRun, error) {
	input, err := decodeStrictJSON[goInfraPlanInput](inputJSON)
	if err != nil {
		return ProcessPreparedRun{}, InvalidProcessInput(err)
	}
	normalized, pullRequestNumber, err := normalizeGoInfraPlanInput(input)
	if err != nil {
		return ProcessPreparedRun{}, InvalidProcessInput(err)
	}
	payload := goInfraProcessPayload{Input: normalized}
	if normalized.Action == goInfraActionReleaseOnMerge {
		pullRequest, err := integration.GetPullRequest(ctx, pullRequestNumber)
		if err != nil {
			return ProcessPreparedRun{}, fmt.Errorf("validate go-infra pull request: %w", err)
		}
		if err := validateGoInfraPullRequest(pullRequest, pullRequestNumber); err != nil {
			return ProcessPreparedRun{}, err
		}
		payload.PullRequest = &pullRequest
	}
	normalizedJSON, err := json.Marshal(normalized)
	if err != nil {
		return ProcessPreparedRun{}, fmt.Errorf("encode normalized go-infra input: %w", err)
	}
	payloadJSON, err := json.Marshal(payload)
	if err != nil {
		return ProcessPreparedRun{}, fmt.Errorf("encode go-infra process plan: %w", err)
	}
	return ProcessPreparedRun{
		Input: normalizedJSON, Payload: payloadJSON,
		Step: goInfraProcessStep(payload), View: goInfraProcessView(payload), Target: goInfraProcessTarget(payload),
	}, nil
}

func executeGoInfraProcess(
	ctx context.Context,
	payloadJSON json.RawMessage,
	checkpoint ProcessCheckpointFunc,
	integration GoInfraGitHubIntegration,
) error {
	payload, err := decodeStrictJSON[goInfraProcessPayload](payloadJSON)
	if err != nil {
		return err
	}
	switch payload.Input.Action {
	case goInfraActionReleaseOnMerge:
		_, err := integration.AddReleaseOnMergeLabel(ctx, payload.PullRequest.Number, payload.PullRequest.HeadSHA)
		return err
	case goInfraActionManualDispatch:
		run, err := integration.DispatchPatchRelease(ctx, payload.Input.DispatchMode == goInfraDispatchModeDryRun)
		if err != nil {
			return err
		}
		if err := checkpointGoInfraProcess(ctx, run, checkpoint); err != nil {
			return err
		}
		return pollGoInfraProcess(ctx, run.ID, checkpoint, integration)
	default:
		return fmt.Errorf("unsupported go-infra action %q", payload.Input.Action)
	}
}

func resumeGoInfraProcess(
	ctx context.Context,
	payloadJSON, state json.RawMessage,
	checkpoint ProcessCheckpointFunc,
	integration GoInfraGitHubIntegration,
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
	return pollGoInfraProcess(ctx, run.ID, checkpoint, integration)
}

func pollGoInfraProcess(
	ctx context.Context,
	runID int64,
	checkpoint ProcessCheckpointFunc,
	integration GoInfraGitHubIntegration,
) error {
	_, err := integration.PollWorkflowRun(ctx, runID, func(run GoInfraWorkflowRun) error {
		return checkpointGoInfraProcess(ctx, run, checkpoint)
	})
	return err
}

func checkpointGoInfraProcess(ctx context.Context, run GoInfraWorkflowRun, checkpoint ProcessCheckpointFunc) error {
	if err := validateGoInfraWorkflowRun(&run); err != nil {
		return err
	}
	state, err := json.Marshal(run)
	if err != nil {
		return fmt.Errorf("encode go-infra workflow run: %w", err)
	}
	return checkpoint(ctx, ProcessRunCheckpoint{
		State: state, External: goInfraExternalRun(run), Progress: goInfraProcessProgress(run),
	})
}

func validateGoInfraProcessRun(run *ProcessRun) error {
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
	if run.Step != goInfraProcessStep(payload) || run.Target != goInfraProcessTarget(payload) ||
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

func goInfraProcessStep(payload goInfraProcessPayload) ProcessRunStep {
	step := ProcessRunStep{Name: "Apply release-on-merge label", Timeout: goInfraLabelTimeout}
	if payload.Input.Action == goInfraActionManualDispatch {
		step.Timeout = goInfraWorkflowTimeout
		if payload.Input.DispatchMode == goInfraDispatchModeDryRun {
			step.Name = "Dispatch patch-release dry run"
		} else {
			step.Name = "Dispatch patch release"
		}
	}
	return step
}

func goInfraProcessTarget(payload goInfraProcessPayload) ProcessRunReference {
	if payload.Input.Action == goInfraActionReleaseOnMerge {
		return ProcessRunReference{
			ID: "pr-" + payload.Input.PullRequest, URL: payload.PullRequest.URL,
			LinkLabel: "Open go-infra PR #" + payload.Input.PullRequest,
		}
	}
	return ProcessRunReference{
		ID: payload.Input.DispatchMode, URL: goInfraWorkflowURL, LinkLabel: "Open go-infra workflow runs",
	}
}

func goInfraExternalRun(run GoInfraWorkflowRun) ProcessRunReference {
	return ProcessRunReference{
		ID: strconv.FormatInt(run.ID, 10), URL: run.URL,
		LinkLabel: "Open go-infra workflow run " + strconv.FormatInt(run.ID, 10),
		Status:    run.Status, Terminal: run.Status == "completed", Succeeded: run.Conclusion == "success",
	}
}

func goInfraProcessProgress(run GoInfraWorkflowRun) ProcessRunProgress {
	progress := ProcessRunProgress{
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

func goInfraProcessView(payload goInfraProcessPayload) ProcessPlanView {
	view := ProcessPlanView{
		Subtitle: "GitHub action · 1 step",
		Request: &ProcessRequestPreview{
			Eyebrow: "GitHub request preview · not sent",
			Target:  goInfraRepository,
		},
	}
	switch payload.Input.Action {
	case goInfraActionReleaseOnMerge:
		pullRequest := payload.PullRequest
		view.IntentTitle = fmt.Sprintf("Apply %s to PR #%d", goInfraReleaseLabel, pullRequest.Number)
		view.IntentBadge = goInfraReleaseLabel
		view.Facts = []ProcessPlanFact{
			{Label: "Pull request", Value: fmt.Sprintf("#%d · %s", pullRequest.Number, pullRequest.Title), Href: pullRequest.URL},
			{Label: "Target", Value: pullRequest.BaseRef, Detail: pullRequest.HeadRef + " @ " + pullRequest.HeadSHA},
		}
		view.Request.Title = "Add pull request label"
		view.Request.Fields = []ProcessRequestField{
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
		view.Facts = []ProcessPlanFact{{Label: "Workflow", Value: goInfraWorkflowFile, Href: goInfraWorkflowURL}, {Label: "Ref", Value: goInfraDefaultRef}}
		view.Request.Title = "Dispatch GitHub Actions workflow"
		view.Request.Fields = []ProcessRequestField{{Name: "workflow", Value: goInfraWorkflowFile}, {Name: "ref", Value: goInfraDefaultRef}, {Name: "dry-run", Value: strconv.FormatBool(dryRun)}}
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
