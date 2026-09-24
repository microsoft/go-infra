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
	"strconv"
	"sync"
	"time"

	"github.com/microsoft/go-infra/releaseui/contract"
	"github.com/microsoft/go-infra/releaseui/coordinator"
	"github.com/microsoft/go-infra/releaseui/releaseflag"
)

const (
	goInfraStateSchema = 1
)

type goInfraSnapshotState struct {
	SchemaVersion int                 `json:"schemaVersion"`
	PullRequest   *GoInfraPullRequest `json:"pullRequest,omitempty"`
	WorkflowRun   *GoInfraWorkflowRun `json:"workflowRun,omitempty"`
	UpdatedAt     time.Time           `json:"updatedAt"`
}

type releaseOnMergeInput struct {
	PullRequest int
}

type goInfraProcessGroup struct {
	github GitHubService
}

type (
	goInfraProcessBase    struct{ github GitHubService }
	releaseOnMergeProcess struct{ goInfraProcessBase }
	dryRunProcess         struct{ goInfraProcessBase }
	publishProcess        struct{ goInfraProcessBase }
)

type goInfraRun struct {
	mu     sync.RWMutex
	github GitHubService
	input  goInfraPlanInput
	state  goInfraSnapshotState
}

// NewProcess creates the microsoft/go-infra release process group.
func NewProcess(github GitHubService) contract.ProcessGroup {
	return &goInfraProcessGroup{github: github}
}

func (g *goInfraProcessGroup) Processes() []contract.Process {
	return []contract.Process{
		&releaseOnMergeProcess{goInfraProcessBase{github: g.github}},
		&dryRunProcess{goInfraProcessBase{github: g.github}},
		&publishProcess{goInfraProcessBase{github: g.github}},
	}
}

func (g *goInfraProcessGroup) ProcessGroupIdentity() *contract.Identity {
	return &contract.Identity{
		Name: "Go infrastructure", Mark: "IN",
		Description:      "Create the next microsoft/go-infra patch release through its GitHub release workflow.",
		DocumentationURL: "https://github.com/microsoft/go-lab/tree/main/docs/release#microsoftgo-infra",
	}
}

func (p *releaseOnMergeProcess) Definition() contract.ProcessDefinition {
	return contract.ProcessDefinition{
		Identity: contract.Identity{
			Name:        "Release on merge",
			Description: "Add release-on-merge to one open, non-fork PR targeting main.",
		},
		ID:               "go-infra-release-on-merge",
		InputPreamble:    "Choose a go-infra release process and review its fixed GitHub action.",
		InputSubmitLabel: "Review GitHub action",
		Notice: &contract.Notice{
			Title:   "The UI does not merge the PR.",
			Message: "The existing workflow creates the patch release only after the labeled PR is merged.",
		},
	}
}

func (p *dryRunProcess) Definition() contract.ProcessDefinition {
	return contract.ProcessDefinition{
		Identity: contract.Identity{
			Name:        "Patch release dry run",
			Description: "Calculate the next v0.0.x version without creating a release.",
		},
		ID:               "go-infra-dry-run",
		InputPreamble:    "Choose a go-infra release process and review its fixed GitHub action.",
		InputSubmitLabel: "Review GitHub action",
		Notice: &contract.Notice{
			Title:   "Dry run does not create a release.",
			Message: "The fixed patch-release workflow runs on main with dry-run enabled.",
		},
	}
}

func (p *publishProcess) Definition() contract.ProcessDefinition {
	return contract.ProcessDefinition{
		Identity: contract.Identity{
			Name:        "Publish patch release",
			Description: "Run the workflow on main and create the next patch release.",
		},
		ID:               "go-infra-publish",
		InputPreamble:    "Choose a go-infra release process and review its fixed GitHub action.",
		InputSubmitLabel: "Review GitHub action",
		Notice: &contract.Notice{
			Title:   "Publishing requires confirmation.",
			Message: "The fixed patch-release workflow runs on main and can create the next release.",
		},
	}
}

func (p goInfraProcessBase) Preflight(ctx context.Context) (warning error, blocking error) {
	if err := validateGoInfraConfiguration(p.github); err != nil {
		return nil, err
	}
	_, err := p.github.Preflight(ctx)
	return nil, err
}

func (p *releaseOnMergeProcess) InputForm(inputs *releaseflag.InputSet) any {
	result := new(releaseOnMergeInput)
	inputs.PositiveIntVar(&result.PullRequest, "pullRequest", releaseflag.FieldOptions{
		Label: "Pull request number", Placeholder: "123",
		Description: "The server verifies that the PR is open, targets main, and does not come from a fork.",
	})
	return result
}

func (p *dryRunProcess) InputForm(*releaseflag.InputSet) any  { return nil }
func (p *publishProcess) InputForm(*releaseflag.InputSet) any { return nil }

func (p *releaseOnMergeProcess) Prepare(
	ctx context.Context,
	inputFormResult any,
) (*contract.StateSnapshot, error) {
	form, ok := inputFormResult.(*releaseOnMergeInput)
	if !ok || form == nil {
		panic("go-infra release-on-merge received an unexpected input type")
	}
	return prepareGoInfra(ctx, p.github, goInfraPlanInput{
		Action: goInfraActionReleaseOnMerge, PullRequest: strconv.Itoa(form.PullRequest),
	})
}

func (p *dryRunProcess) Prepare(ctx context.Context, inputFormResult any) (*contract.StateSnapshot, error) {
	if inputFormResult != nil {
		panic("go-infra dry run received an input value")
	}
	return prepareGoInfra(ctx, p.github, goInfraPlanInput{
		Action: goInfraActionManualDispatch, DispatchMode: goInfraDispatchModeDryRun,
	})
}

func (p *publishProcess) Prepare(ctx context.Context, inputFormResult any) (*contract.StateSnapshot, error) {
	if inputFormResult != nil {
		panic("go-infra publish received an input value")
	}
	return prepareGoInfra(ctx, p.github, goInfraPlanInput{
		Action: goInfraActionManualDispatch, DispatchMode: goInfraDispatchModePublish,
	})
}

func prepareGoInfra(
	ctx context.Context,
	github GitHubService,
	input goInfraPlanInput,
) (*contract.StateSnapshot, error) {
	if err := validateGoInfraConfiguration(github); err != nil {
		return nil, err
	}
	normalized, pullRequestNumber, err := normalizeGoInfraPlanInput(input)
	if err != nil {
		return nil, errors.Join(err, contract.ErrInvalidInput)
	}
	state := goInfraSnapshotState{SchemaVersion: goInfraStateSchema, UpdatedAt: time.Now().UTC()}
	if normalized.Action == goInfraActionReleaseOnMerge {
		pullRequest, err := github.GetPullRequest(ctx, pullRequestNumber)
		if err != nil {
			return nil, fmt.Errorf("validate go-infra pull request: %w", err)
		}
		if err := validateGoInfraPullRequest(pullRequest, pullRequestNumber); err != nil {
			return nil, err
		}
		state.PullRequest = &pullRequest
	}
	return encodeGoInfraSnapshot(normalized, state)
}

func loadGoInfraRun(
	github GitHubService,
	snapshot *contract.StateSnapshot,
	match func(goInfraPlanInput) bool,
) (contract.Run, error) {
	input, state, err := decodeGoInfraSnapshot(snapshot)
	if err != nil {
		return nil, err
	}
	if !match(input) {
		return nil, errors.New("go-infra snapshot belongs to a different process")
	}
	if err := validateGoInfraState(input, state); err != nil {
		return nil, err
	}
	return &goInfraRun{github: github, input: input, state: state}, nil
}

func (p *releaseOnMergeProcess) Load(snapshot *contract.StateSnapshot) (contract.Run, error) {
	return loadGoInfraRun(p.github, snapshot, func(input goInfraPlanInput) bool {
		return input.Action == goInfraActionReleaseOnMerge
	})
}

func (p *dryRunProcess) Load(snapshot *contract.StateSnapshot) (contract.Run, error) {
	return loadGoInfraRun(p.github, snapshot, func(input goInfraPlanInput) bool {
		return input.Action == goInfraActionManualDispatch &&
			input.DispatchMode == goInfraDispatchModeDryRun
	})
}

func (p *publishProcess) Load(snapshot *contract.StateSnapshot) (contract.Run, error) {
	return loadGoInfraRun(p.github, snapshot, func(input goInfraPlanInput) bool {
		return input.Action == goInfraActionManualDispatch &&
			input.DispatchMode == goInfraDispatchModePublish
	})
}

func (r *goInfraRun) TakeSnapshot() *contract.StateSnapshot {
	r.mu.RLock()
	defer r.mu.RUnlock()
	snapshot, err := encodeGoInfraSnapshot(r.input, r.state)
	if err != nil {
		panic(fmt.Sprintf("encode go-infra snapshot: %v", err))
	}
	return snapshot
}

func (r *goInfraRun) TakeView() *contract.RunView {
	r.mu.RLock()
	defer r.mu.RUnlock()
	view := &contract.RunView{
		Test: goInfraProcessIsTest(r.input), Summary: "Ready",
		Detail: goInfraProcessName(r.input), UpdatedAt: r.state.UpdatedAt,
	}
	if r.state.WorkflowRun == nil {
		return view
	}
	run := r.state.WorkflowRun
	view.Summary = "GitHub workflow is queued"
	view.Detail = fmt.Sprintf("Waiting for run %d to start", run.ID)
	switch run.Status {
	case "in_progress":
		view.Summary = "GitHub workflow is running"
		view.Detail = fmt.Sprintf("Run %d is in progress", run.ID)
	case "completed":
		view.Summary = "GitHub workflow completed"
		view.Detail = fmt.Sprintf("Run %d completed with conclusion %s", run.ID, run.Conclusion)
	}
	return view
}

func (r *goInfraRun) Plan() *contract.Plan {
	r.mu.RLock()
	defer r.mu.RUnlock()
	plan := &contract.Plan{Subtitle: "GitHub action · 1 step", ExecutionButtonLabel: "Run GitHub action"}
	switch r.input.Action {
	case goInfraActionReleaseOnMerge:
		plan.ExecutionButtonLabel = "Apply release-on-merge"
		plan.Facts = []contract.PlanFact{
			{Label: "Repository", Value: goInfraRepository},
			{
				Label: "Pull request", Value: "#" + r.input.PullRequest,
				Detail: fmt.Sprintf(`<a href="%s" target="_blank" rel="noreferrer">Open pull request</a>`, r.state.PullRequest.URL),
			},
			{Label: "Action", Value: "Add release-on-merge label"},
		}
	case goInfraActionManualDispatch:
		plan.Facts = []contract.PlanFact{
			{Label: "Repository", Value: goInfraRepository},
			{Label: "Workflow", Value: goInfraWorkflowFile},
			{Label: "Branch", Value: goInfraDefaultRef},
			{Label: "Dry run", Value: strconv.FormatBool(r.input.DispatchMode == goInfraDispatchModeDryRun)},
		}
		if r.input.DispatchMode == goInfraDispatchModeDryRun {
			plan.ExecutionButtonLabel = "Run patch release dry run"
		} else {
			plan.ExecutionButtonLabel = "Publish patch release"
		}
	}
	return plan
}

func (r *goInfraRun) Build(_ context.Context, checkpoint contract.CheckpointFunc) ([]*coordinator.Step, error) {
	if err := validateGoInfraConfiguration(r.github); err != nil {
		return nil, err
	}
	r.mu.RLock()
	input := r.input
	state := r.state
	r.mu.RUnlock()
	action := func(ctx context.Context) error {
		switch input.Action {
		case goInfraActionReleaseOnMerge:
			_, err := r.github.AddReleaseOnMergeLabel(ctx, state.PullRequest.Number, state.PullRequest.HeadSHA)
			return err
		case goInfraActionManualDispatch:
			if state.WorkflowRun == nil {
				run, err := r.github.DispatchPatchRelease(ctx, input.DispatchMode == goInfraDispatchModeDryRun)
				if err != nil {
					return err
				}
				if err := r.checkpoint(ctx, run, checkpoint); err != nil {
					return err
				}
				state.WorkflowRun = &run
			}
			_, err := r.github.PollWorkflowRun(ctx, state.WorkflowRun.ID, func(run GoInfraWorkflowRun) error {
				return r.checkpoint(ctx, run, checkpoint)
			})
			return err
		default:
			return fmt.Errorf("unsupported go-infra action %q", input.Action)
		}
	}
	return []*coordinator.Step{goInfraProcessStep(input, action)}, nil
}

func (r *goInfraRun) checkpoint(ctx context.Context, run GoInfraWorkflowRun, checkpoint contract.CheckpointFunc) error {
	if err := validateGoInfraWorkflowRun(&run); err != nil {
		return err
	}
	r.mu.Lock()
	r.state.WorkflowRun = &run
	r.state.UpdatedAt = time.Now().UTC()
	r.mu.Unlock()
	if checkpoint != nil {
		checkpoint()
	}
	return ctx.Err()
}

func encodeGoInfraSnapshot(input goInfraPlanInput, state goInfraSnapshotState) (*contract.StateSnapshot, error) {
	inputJSON, err := json.Marshal(input)
	if err != nil {
		return nil, fmt.Errorf("encode go-infra input: %w", err)
	}
	stateJSON, err := json.Marshal(state)
	if err != nil {
		return nil, fmt.Errorf("encode go-infra state: %w", err)
	}
	return &contract.StateSnapshot{Input: inputJSON, State: stateJSON}, nil
}

func decodeGoInfraSnapshot(snapshot *contract.StateSnapshot) (goInfraPlanInput, goInfraSnapshotState, error) {
	if snapshot == nil {
		return goInfraPlanInput{}, goInfraSnapshotState{}, errors.New("go-infra snapshot is nil")
	}
	input, err := decodeStrictJSON[goInfraPlanInput](snapshot.Input)
	if err != nil {
		return goInfraPlanInput{}, goInfraSnapshotState{}, fmt.Errorf("decode go-infra input: %w", err)
	}
	state, err := decodeStrictJSON[goInfraSnapshotState](snapshot.State)
	if err != nil {
		return goInfraPlanInput{}, goInfraSnapshotState{}, fmt.Errorf("decode go-infra state: %w", err)
	}
	if state.SchemaVersion != goInfraStateSchema {
		return goInfraPlanInput{}, goInfraSnapshotState{}, fmt.Errorf("unsupported go-infra state schema %d", state.SchemaVersion)
	}
	return input, state, nil
}

func validateGoInfraState(input goInfraPlanInput, state goInfraSnapshotState) error {
	normalized, pullRequestNumber, err := normalizeGoInfraPlanInput(input)
	if err != nil || normalized != input {
		return errors.New("go-infra process input is invalid")
	}
	switch input.Action {
	case goInfraActionReleaseOnMerge:
		if state.PullRequest == nil {
			return errors.New("release-on-merge plan has no pull request")
		}
		if err := validateGoInfraPullRequest(*state.PullRequest, pullRequestNumber); err != nil {
			return err
		}
		if state.WorkflowRun != nil {
			return errors.New("release-on-merge plan contains a workflow run")
		}
	case goInfraActionManualDispatch:
		if state.PullRequest != nil {
			return errors.New("manual-dispatch plan contains a pull request")
		}
		if state.WorkflowRun != nil {
			if err := validateGoInfraWorkflowRun(state.WorkflowRun); err != nil {
				return err
			}
		}
	default:
		return fmt.Errorf("unsupported go-infra action %q", input.Action)
	}
	return nil
}

func validateGoInfraConfiguration(github GitHubService) error {
	if github == nil {
		return errors.New("go-infra integration is incomplete")
	}
	return nil
}

func goInfraProcessIsTest(input goInfraPlanInput) bool {
	return input.Action == goInfraActionManualDispatch && input.DispatchMode == goInfraDispatchModeDryRun
}

func goInfraProcessName(input goInfraPlanInput) string {
	if input.Action == goInfraActionReleaseOnMerge {
		return "Release on merge"
	}
	if input.DispatchMode == goInfraDispatchModeDryRun {
		return "Patch release dry run"
	}
	return "Publish patch release"
}

func goInfraProcessStep(input goInfraPlanInput, action func(context.Context) error) *coordinator.Step {
	name := "Apply release-on-merge label"
	timeout := goInfraLabelTimeout
	if input.Action == goInfraActionManualDispatch {
		timeout = goInfraWorkflowTimeout
		if input.DispatchMode == goInfraDispatchModeDryRun {
			name = "Dispatch patch-release dry run"
		} else {
			name = "Dispatch patch release"
		}
	}
	return coordinator.NewRootStep(name, timeout, action)
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
	_ contract.ProcessGroup = (*goInfraProcessGroup)(nil)
	_ contract.Process      = (*releaseOnMergeProcess)(nil)
	_ contract.Process      = (*dryRunProcess)(nil)
	_ contract.Process      = (*publishProcess)(nil)
	_ contract.Run          = (*goInfraRun)(nil)
)
