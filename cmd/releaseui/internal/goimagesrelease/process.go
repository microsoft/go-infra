// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package goimagesrelease

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"reflect"
	"strconv"
	"strings"
	"time"

	"github.com/microsoft/go-infra/cmd/releaseui/internal/goimagessession"
	"github.com/microsoft/go-infra/cmd/releaseui/internal/goimagesworkflow"
	releaseui "github.com/microsoft/go-infra/releaseui"
	"github.com/microsoft/go-infra/releaseui/coordinator"
)

const (
	goImagesProcessID       = "go-images"
	goImagesPipelineName    = "microsoft-go-images (official)"
	goImagesPipelineOrg     = "dnceng"
	goImagesPipelineProject = "internal"
)

// PlanInput is the browser-controlled input for a go-images release.
type PlanInput struct {
	Mode          goimagesworkflow.Mode `json:"mode"`
	SourceBuildID string                `json:"sourceBuildId,omitempty"`
}

// GoImagesSource is the exact current microsoft/main source selected entirely by the process.
type GoImagesSource struct {
	Branch   string   `json:"branch"`
	Commit   string   `json:"commit"`
	Versions []string `json:"versions,omitempty"`
}

// GoImagesRollbackSource describes a validated successful build whose artifacts may be republished.
type GoImagesRollbackSource struct {
	BuildID  int
	URL      string
	Versions []string
}

// GoImagesReadOnlyIntegration resolves current main and validates rollback builds without mutation.
type GoImagesReadOnlyIntegration struct {
	Preflight            func(context.Context) (string, error)
	ResolveCurrentSource func(context.Context) (GoImagesSource, error)
	ValidateRollback     func(context.Context, int) (GoImagesRollbackSource, error)
}

// GoImagesExecutionIntegration constructs the fixed pipeline 1023 execution service.
type GoImagesExecutionIntegration struct {
	NewService func(GoImagesExecutionRequest) (goimagesworkflow.Service, error)
}

// GoImagesExecutionRequest binds one real run to a confirmed durable plan.
type GoImagesExecutionRequest struct {
	Mode                 goimagesworkflow.Mode
	SessionID            string
	ExecutionDigest      string
	Versions             []string
	SourceBuildID        string
	SourceVersion        string
	PreviousQueueAttempt bool
}

type goImagesProcess struct {
	readOnly  GoImagesReadOnlyIntegration
	execution *GoImagesExecutionIntegration
}

type goImagesRun struct {
	process *goImagesProcess
	state   *releaseui.ReleaseRunState
}

type goImagesProcessPayload struct {
	Document       goimagessession.Document `json:"document"`
	Source         GoImagesSource           `json:"source"`
	RollbackSource *GoImagesRollbackSource  `json:"rollbackSource,omitempty"`
}

// NewProcess creates the fixed pipeline 1023 release process. A nil execution integration keeps
// planning and simulation available while disabling real pipeline execution.
func NewProcess(
	readOnly GoImagesReadOnlyIntegration,
	execution *GoImagesExecutionIntegration,
) releaseui.ReleaseProcess {
	return &goImagesProcess{readOnly: readOnly, execution: execution}
}

func (p *goImagesProcess) Definition() releaseui.ProcessDefinition {
	return releaseui.ProcessDefinition{
		ID: goImagesProcessID, Name: "Go images", Mark: "GI",
		Description:      "Build, sign, publish, test, or republish the Microsoft Build of Go container images.",
		DocumentationURL: "https://github.com/microsoft/go-lab/tree/main/docs/release#golang-toolset-images",
		Workflow: releaseui.ProcessWorkflow{
			Heading: "Choose release type", Description: "Only rollback accepts a pipeline input.",
			SubmitLabel: "Prepare release", CanSimulate: true,
			Inputs: []releaseui.ProcessInput{
				{
					ID: "mode", Type: "choice", Label: "Release type", Default: "normal",
					Options: []releaseui.ProcessInputOption{
						{Value: "normal", Name: "Normal release", Mark: "N", Description: "Build current microsoft/main and publish to public/. All parameters are locked.", NoticeTitle: "Normal release is locked.", Notice: "Current main, current-build artifacts, and public/ are selected server-side."},
						{Value: "rollback", Name: "Rollback / republish", Mark: "R", Description: "Republish artifacts from one successful pipeline 1023 build to public/.", NoticeTitle: "Only the source build is editable.", Notice: "The server locks current main and public/, then validates the selected build."},
						{Value: "test", Name: "Test release", Mark: "T", Description: "Build current microsoft/main and publish only under the dev/ prefix.", NoticeTitle: "Test release is locked to dev/.", Notice: "Current main is built normally, but publication is isolated under the dev/ prefix."},
					},
				},
				{
					ID: "sourceBuildId", Type: "number", Label: "Source build ID", Placeholder: "3034159",
					Description: "The server verifies that this is a successful pipeline 1023 run which produced its own artifacts.",
					VisibleWhen: &releaseui.ProcessCondition{InputID: "mode", Equals: "rollback"},
				},
			},
		},
	}
}

func (p *goImagesProcess) Preflight(ctx context.Context) (releaseui.ProcessReadiness, error) {
	if err := p.validateConfiguration(); err != nil {
		return releaseui.ProcessReadiness{Details: err.Error()}, nil
	}
	details, err := p.readOnly.Preflight(ctx)
	readiness := releaseui.ProcessReadiness{
		PlanningEnabled: err == nil, ExecutionEnabled: err == nil && p.execution != nil, Details: details,
	}
	if err == nil && p.execution == nil {
		readiness.Details = strings.TrimSpace(details + " Real pipeline execution is disabled.")
	}
	return readiness, err
}

func (p *goImagesProcess) Prepare(ctx context.Context, inputJSON json.RawMessage) (releaseui.ReleaseRun, error) {
	prepared, err := p.prepare(ctx, inputJSON)
	if err != nil {
		return nil, err
	}
	state, err := releaseui.NewReleaseRunState(goImagesProcessID, prepared)
	if err != nil {
		return nil, fmt.Errorf("create go-images release run: %w", err)
	}
	return p.Restore(state)
}

func (p *goImagesProcess) prepare(ctx context.Context, inputJSON json.RawMessage) (releaseui.ReleasePlan, error) {
	if err := p.validateConfiguration(); err != nil {
		return releaseui.ReleasePlan{}, err
	}
	input, err := decodeStrictJSON[PlanInput](inputJSON)
	if err != nil {
		return releaseui.ReleasePlan{}, releaseui.InvalidProcessInput(err)
	}
	normalized, err := normalizePlanInput(input)
	if err != nil {
		return releaseui.ReleasePlan{}, releaseui.InvalidProcessInput(err)
	}
	if _, err := p.readOnly.Preflight(ctx); err != nil {
		return releaseui.ReleasePlan{}, fmt.Errorf("azure preflight failed: %w", err)
	}
	source, err := p.readOnly.ResolveCurrentSource(ctx)
	if err != nil {
		return releaseui.ReleasePlan{}, fmt.Errorf("resolve current microsoft/main: %w", err)
	}
	if err := validateCurrentSource(source); err != nil {
		return releaseui.ReleasePlan{}, err
	}
	source.Versions, err = normalizeResolvedVersions(source.Versions)
	if err != nil {
		return releaseui.ReleasePlan{}, err
	}
	versions := append([]string(nil), source.Versions...)
	var rollbackSource *GoImagesRollbackSource
	if normalized.Mode == goimagesworkflow.ModeRollback {
		buildID, _ := strconv.Atoi(normalized.SourceBuildID)
		validated, err := p.readOnly.ValidateRollback(ctx, buildID)
		if err != nil {
			return releaseui.ReleasePlan{}, fmt.Errorf("validate rollback source: %w", err)
		}
		if validated.BuildID != buildID {
			return releaseui.ReleasePlan{}, errors.New("rollback validation returned a different build")
		}
		validated.Versions, err = normalizeResolvedVersions(validated.Versions)
		if err != nil {
			return releaseui.ReleasePlan{}, err
		}
		rollbackSource = &validated
		versions = append([]string(nil), validated.Versions...)
	}
	workflowInput := &goimagesworkflow.Input{
		Versions: versions, Mode: normalized.Mode, SourceVersion: source.Commit,
		SourceBuildID: normalized.SourceBuildID,
	}
	steps, state, err := goimagesworkflow.NewGraphWithCheckpoint(
		workflowInput, nil, disabledGoImagesService{}, nil,
	)
	if err != nil {
		return releaseui.ReleasePlan{}, fmt.Errorf("create go-images plan: %w", err)
	}
	document, err := goimagessession.NewDocument(workflowInput, state, steps, time.Now())
	if err != nil {
		return releaseui.ReleasePlan{}, fmt.Errorf("create durable release session: %w", err)
	}
	payloadJSON, err := json.Marshal(goImagesProcessPayload{
		Document: *document, Source: source, RollbackSource: rollbackSource,
	})
	if err != nil {
		return releaseui.ReleasePlan{}, fmt.Errorf("encode go-images process plan: %w", err)
	}
	normalizedJSON, err := json.Marshal(normalized)
	if err != nil {
		return releaseui.ReleasePlan{}, fmt.Errorf("encode normalized go-images input: %w", err)
	}
	runSteps := goImagesProcessRunSteps(document.Plan)
	parameters, err := goimagesworkflow.PipelineParameters(normalized.Mode, normalized.SourceBuildID)
	if err != nil {
		return releaseui.ReleasePlan{}, err
	}
	return releaseui.ReleasePlan{
		Test: normalized.Mode == goimagesworkflow.ModeTest, Input: normalizedJSON, Payload: payloadJSON,
		SessionID: document.ID, Steps: runSteps,
		View:   goImagesPlanView(normalized, source, rollbackSource, parameters, len(steps), false),
		Target: goImagesProcessTarget(),
	}, nil
}

func (p *goImagesProcess) Restore(state *releaseui.ReleaseRunState) (releaseui.ReleaseRun, error) {
	if err := p.validate(state); err != nil {
		return nil, err
	}
	return &goImagesRun{process: p, state: releaseui.CloneReleaseRunState(state)}, nil
}

func (r *goImagesRun) Snapshot() *releaseui.ReleaseRunState {
	return releaseui.CloneReleaseRunState(r.state)
}

func (r *goImagesRun) Steps(
	ctx context.Context,
	checkpoint releaseui.CheckpointFunc,
) ([]*coordinator.Step, error) {
	run := r.Snapshot()
	if err := r.process.validate(run); err != nil {
		return nil, err
	}
	payload, err := decodeStrictJSON[goImagesProcessPayload](run.Payload)
	if err != nil {
		return nil, err
	}
	state := payload.Document.State
	if len(run.Checkpoint) > 0 {
		state, err = decodeStrictJSON[goimagesworkflow.State](run.Checkpoint)
		if err != nil {
			return nil, fmt.Errorf("decode go-images checkpoint: %w", err)
		}
	}
	var service goimagesworkflow.Service = disabledGoImagesService{}
	workflowCheckpoint := goimagesworkflow.CheckpointFunc(nil)
	if checkpoint != nil && !run.Complete {
		if r.process.execution == nil || r.process.execution.NewService == nil {
			return nil, errors.New("real go-images execution is not enabled")
		}
		if state.BuildID == "" {
			current, err := r.process.readOnly.ResolveCurrentSource(ctx)
			if err != nil {
				return nil, fmt.Errorf("re-resolve current microsoft/main: %w", err)
			}
			if current.Branch != goimagesworkflow.SourceBranch || current.Commit != payload.Document.Input.SourceVersion {
				return nil, errors.New("microsoft/main changed after this plan was prepared; refresh the plan before queueing")
			}
			if payload.Document.Input.Mode == goimagesworkflow.ModeRollback {
				buildID, _ := strconv.Atoi(payload.Document.Input.SourceBuildID)
				if _, err := r.process.readOnly.ValidateRollback(ctx, buildID); err != nil {
					return nil, fmt.Errorf("revalidate rollback source: %w", err)
				}
			}
		}
		service, err = r.process.execution.NewService(GoImagesExecutionRequest{
			Mode: payload.Document.Input.Mode, SessionID: payload.Document.ID, ExecutionDigest: run.Digest,
			Versions:      append([]string(nil), payload.Document.Input.Versions...),
			SourceBuildID: payload.Document.Input.SourceBuildID, SourceVersion: payload.Document.Input.SourceVersion,
			PreviousQueueAttempt: state.QueueAttempted,
		})
		if err != nil {
			return nil, fmt.Errorf("create go-images execution service: %w", err)
		}
		workflowCheckpoint = func(ctx context.Context, state *goimagesworkflow.State) error {
			stateJSON, err := json.Marshal(state)
			if err != nil {
				return fmt.Errorf("encode go-images checkpoint: %w", err)
			}
			return checkpoint(ctx, releaseui.ReleaseCheckpoint{
				State: stateJSON, External: goImagesExternalRun(state),
			})
		}
	}
	input := payload.Document.Input
	steps, _, err := goimagesworkflow.NewGraphWithCheckpoint(&input, &state, service, workflowCheckpoint)
	if err != nil {
		return nil, err
	}
	plan, err := goimagessession.NewPlan(steps)
	if err != nil {
		return nil, err
	}
	if err := payload.Document.MatchesPlan(plan); err != nil {
		return nil, err
	}
	return steps, nil
}

func (p *goImagesProcess) validate(run *releaseui.ReleaseRunState) error {
	if run == nil || run.ProcessID != goImagesProcessID {
		return errors.New("go-images process run has an invalid process ID")
	}
	payload, err := decodeStrictJSON[goImagesProcessPayload](run.Payload)
	if err != nil {
		return fmt.Errorf("decode go-images process payload: %w", err)
	}
	if err := payload.Document.Validate(); err != nil {
		return fmt.Errorf("validate go-images document: %w", err)
	}
	input, err := decodeStrictJSON[PlanInput](run.Input)
	if err != nil {
		return fmt.Errorf("decode go-images process input: %w", err)
	}
	normalized, err := normalizePlanInput(input)
	if err != nil || normalized != input || normalized.Mode != payload.Document.Input.Mode ||
		normalized.SourceBuildID != payload.Document.Input.SourceBuildID {

		return errors.New("go-images process input is invalid")
	}
	if run.SessionID != payload.Document.ID {
		return errors.New("go-images process session ID does not match its document")
	}
	if run.Test != (input.Mode == goimagesworkflow.ModeTest) {
		return errors.New("go-images process test classification is invalid")
	}
	if err := validateCurrentSource(payload.Source); err != nil {
		return err
	}
	normalizedSourceVersions, err := normalizeResolvedVersions(payload.Source.Versions)
	if err != nil || !reflect.DeepEqual(normalizedSourceVersions, payload.Source.Versions) ||
		payload.Source.Commit != payload.Document.Input.SourceVersion ||
		input.Mode != goimagesworkflow.ModeRollback && !reflect.DeepEqual(payload.Source.Versions, payload.Document.Input.Versions) {

		return errors.New("go-images source does not match its document")
	}
	if err := validateGoImagesRollbackPayload(input, payload); err != nil {
		return err
	}
	initialState, err := goimagesworkflow.NewState(&payload.Document.Input)
	if err != nil || *initialState != payload.Document.State {
		return errors.New("go-images payload document does not contain its initial state")
	}
	wantSteps := goImagesProcessRunSteps(payload.Document.Plan)
	parameters, err := goimagesworkflow.PipelineParameters(input.Mode, input.SourceBuildID)
	if err != nil {
		return err
	}
	if !reflect.DeepEqual(run.Steps, wantSteps) || run.Target != goImagesProcessTarget() ||
		!reflect.DeepEqual(run.View, goImagesPlanView(input, payload.Source, payload.RollbackSource, parameters, len(wantSteps), false)) {

		return errors.New("go-images process plan does not match its fixed policy")
	}
	if len(run.Checkpoint) == 0 {
		if run.External != nil {
			return errors.New("go-images process has an external run without state")
		}
		if run.Complete && run.Result == "succeeded" {
			return errors.New("successful go-images process has no checkpoint")
		}
		return nil
	}
	state, err := decodeStrictJSON[goimagesworkflow.State](run.Checkpoint)
	if err != nil {
		return fmt.Errorf("decode go-images process checkpoint: %w", err)
	}
	inputCopy := payload.Document.Input
	steps, _, err := goimagesworkflow.NewGraphWithCheckpoint(
		&inputCopy, &state, disabledGoImagesService{}, nil,
	)
	if err != nil {
		return fmt.Errorf("validate go-images process checkpoint: %w", err)
	}
	plan, err := goimagessession.NewPlan(steps)
	if err != nil {
		return err
	}
	if err := payload.Document.MatchesPlan(plan); err != nil {
		return err
	}
	wantExternal := goImagesExternalRun(&state)
	if !equalProcessRunReference(run.External, wantExternal) {
		return errors.New("go-images checkpoint does not match its external run")
	}
	if run.Complete && run.Result == "succeeded" && (!state.Complete || state.Result != "succeeded") {
		return errors.New("successful go-images process has incomplete state")
	}
	if run.Complete && state.Complete && run.Result != "uncertain" && run.Result != state.Result {
		return errors.New("go-images process result does not match its state")
	}
	return nil
}

func (p *goImagesProcess) validateConfiguration() error {
	if p.readOnly.Preflight == nil || p.readOnly.ResolveCurrentSource == nil || p.readOnly.ValidateRollback == nil {
		return errors.New("go-images read-only integration is incomplete")
	}
	if p.execution != nil && p.execution.NewService == nil {
		return errors.New("go-images execution integration is incomplete")
	}
	return nil
}

func validateGoImagesRollbackPayload(input PlanInput, payload goImagesProcessPayload) error {
	if input.Mode != goimagesworkflow.ModeRollback {
		if payload.RollbackSource != nil {
			return errors.New("non-rollback go-images process has a rollback source")
		}
		return nil
	}
	buildID, _ := strconv.Atoi(input.SourceBuildID)
	if payload.RollbackSource == nil || payload.RollbackSource.BuildID != buildID ||
		!strings.HasPrefix(payload.RollbackSource.URL, "https://") ||
		!reflect.DeepEqual(payload.RollbackSource.Versions, payload.Document.Input.Versions) {

		return errors.New("go-images rollback source does not match its document")
	}
	return nil
}

func goImagesProcessRunSteps(plan goimagessession.Plan) []releaseui.ReleaseStep {
	steps := make([]releaseui.ReleaseStep, len(plan.Steps))
	for index, step := range plan.Steps {
		steps[index] = releaseui.ReleaseStep{
			Name: step.Name, DependsOn: append([]string(nil), step.DependsOn...), Timeout: time.Duration(step.TimeoutNanos),
		}
	}
	return steps
}

func goImagesProcessTarget() releaseui.ReleaseReference {
	return releaseui.ReleaseReference{
		ID:        strconv.Itoa(goimagesworkflow.DefinitionID),
		URL:       "https://dev.azure.com/dnceng/internal/_build?definitionId=1023",
		LinkLabel: "Open go-images pipeline 1023",
	}
}

func goImagesExternalRun(state *goimagesworkflow.State) *releaseui.ReleaseReference {
	if state == nil || state.BuildID == "" {
		return nil
	}
	reference := &releaseui.ReleaseReference{
		ID:        state.BuildID,
		URL:       "https://dev.azure.com/dnceng/internal/_build/results?buildId=" + state.BuildID,
		LinkLabel: "Open Azure DevOps run " + state.BuildID,
		Status:    "running",
	}
	if state.Complete {
		reference.Status = state.Result
		reference.Terminal = true
		reference.Succeeded = state.Result == "succeeded"
	}
	return reference
}

func equalProcessRunReference(left, right *releaseui.ReleaseReference) bool {
	if left == nil || right == nil {
		return left == nil && right == nil
	}
	return *left == *right
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
	_ releaseui.ReleaseProcess = (*goImagesProcess)(nil)
	_ releaseui.ReleaseRun     = (*goImagesRun)(nil)
)
