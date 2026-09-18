// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package goimages

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

	releaseui "github.com/microsoft/go-infra/releaseui"
	"github.com/microsoft/go-infra/releaseui/contract"
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
	Mode          Mode   `json:"mode"`
	SourceBuildID string `json:"sourceBuildId,omitempty"`
}

type rollbackVariantInput struct {
	SourceBuildID int
}

// Source is the exact current microsoft/main source selected entirely by the process.
type Source struct {
	Branch   string   `json:"branch"`
	Commit   string   `json:"commit"`
	Versions []string `json:"versions,omitempty"`
}

// ProcessService is the Azure behavior required by the Go-images release process.
type ProcessService interface {
	Preflight(context.Context) (string, error)
	ResolveCurrentSource(context.Context) (Source, error)
	ValidateRollback(context.Context, int) (RollbackSource, error)
	NewRunService(RunRequest) (RunService, error)
}

// RunRequest binds one real run to a confirmed durable plan.
type RunRequest struct {
	Mode                 Mode
	SessionID            string
	ExecutionDigest      string
	Versions             []string
	SourceBuildID        string
	SourceVersion        string
	PreviousQueueAttempt bool
}

type goImagesProcess struct {
	service ProcessService
}

type goImagesRun struct {
	process *goImagesProcess
	state   *contract.State
}

type goImagesProcessPayload struct {
	Document       Document        `json:"document"`
	Source         Source          `json:"source"`
	RollbackSource *RollbackSource `json:"rollbackSource,omitempty"`
}

// NewProcess creates the fixed pipeline 1023 release process.
func NewProcess(service ProcessService) contract.Process {
	return &goImagesProcess{service: service}
}

func (p *goImagesProcess) Definition() contract.Definition {
	rollbackInputs := newRollbackInputSet(new(rollbackVariantInput)).Inputs()
	return contract.Definition{
		ID: goImagesProcessID, Name: "Go images", Mark: "GI",
		Description:      "Build, sign, publish, test, or republish the Microsoft Build of Go container images.",
		DocumentationURL: "https://github.com/microsoft/go-lab/tree/main/docs/release#golang-toolset-images",
		Workflow: contract.Workflow{
			Heading: "Choose release type", Description: "Select a release process. Only rollback accepts an input.",
			SubmitLabel: "Prepare release", CanSimulate: true,
			Variants: []contract.Variant{
				{
					ID: "normal", Name: "Normal release",
					Description: "Build current microsoft/main and publish to public/. All parameters are locked.",
					NoticeTitle: "Normal release is locked.",
					Notice:      "Current main, current-build artifacts, and public/ are selected server-side.",
				},
				{
					ID: "rollback", Name: "Rollback / republish",
					Description: "Republish artifacts from one successful pipeline 1023 build to public/.",
					Inputs:      rollbackInputs,
					NoticeTitle: "Only the source build is editable.",
					Notice:      "The server locks current main and public/, then validates the selected build.",
				},
				{
					ID: "test", Name: "Test release",
					Description: "Build current microsoft/main and publish only under the dev/ prefix.",
					NoticeTitle: "Test release is locked to dev/.",
					Notice:      "Current main is built normally, but publication is isolated under the dev/ prefix.",
				},
			},
		},
	}
}

func (p *goImagesProcess) Preflight(ctx context.Context) (contract.Readiness, error) {
	if err := p.validateConfiguration(); err != nil {
		return contract.Readiness{Details: err.Error()}, nil
	}
	details, err := p.service.Preflight(ctx)
	readiness := contract.Readiness{
		PlanningEnabled: err == nil, ExecutionEnabled: err == nil, Details: details,
	}
	return readiness, err
}

func (p *goImagesProcess) Prepare(ctx context.Context, selection contract.Selection) (contract.Run, error) {
	input, err := goImagesPlanInput(selection)
	if err != nil {
		return nil, contract.InvalidInput(err)
	}
	prepared, err := p.prepare(ctx, selection.VariantID, input)
	if err != nil {
		return nil, err
	}
	state, err := releaseui.NewReleaseRunState(goImagesProcessID, prepared)
	if err != nil {
		return nil, fmt.Errorf("create go-images release run: %w", err)
	}
	return p.Restore(state)
}

func (p *goImagesProcess) prepare(ctx context.Context, variantID string, input PlanInput) (contract.Plan, error) {
	if err := p.validateConfiguration(); err != nil {
		return contract.Plan{}, err
	}
	normalized, err := normalizePlanInput(input)
	if err != nil {
		return contract.Plan{}, contract.InvalidInput(err)
	}
	if _, err := p.service.Preflight(ctx); err != nil {
		return contract.Plan{}, fmt.Errorf("azure preflight failed: %w", err)
	}
	source, err := p.service.ResolveCurrentSource(ctx)
	if err != nil {
		return contract.Plan{}, fmt.Errorf("resolve current microsoft/main: %w", err)
	}
	if err := validateCurrentSource(source); err != nil {
		return contract.Plan{}, err
	}
	source.Versions, err = normalizeResolvedVersions(source.Versions)
	if err != nil {
		return contract.Plan{}, err
	}
	versions := append([]string(nil), source.Versions...)
	var rollbackSource *RollbackSource
	if normalized.Mode == ModeRollback {
		buildID, _ := strconv.Atoi(normalized.SourceBuildID)
		validated, err := p.service.ValidateRollback(ctx, buildID)
		if err != nil {
			return contract.Plan{}, fmt.Errorf("validate rollback source: %w", err)
		}
		if validated.BuildID != buildID {
			return contract.Plan{}, errors.New("rollback validation returned a different build")
		}
		validated.Versions, err = normalizeResolvedVersions(validated.Versions)
		if err != nil {
			return contract.Plan{}, err
		}
		rollbackSource = &validated
		versions = append([]string(nil), validated.Versions...)
	}
	workflowInput := &Input{
		Versions: versions, Mode: normalized.Mode, SourceVersion: source.Commit,
		SourceBuildID: normalized.SourceBuildID,
	}
	steps, state, err := NewGraphWithCheckpoint(
		workflowInput, nil, disabledGoImagesService{}, nil,
	)
	if err != nil {
		return contract.Plan{}, fmt.Errorf("create go-images plan: %w", err)
	}
	document, err := NewDocument(workflowInput, state, time.Now())
	if err != nil {
		return contract.Plan{}, fmt.Errorf("create durable release session: %w", err)
	}
	payloadJSON, err := json.Marshal(goImagesProcessPayload{
		Document: *document, Source: source, RollbackSource: rollbackSource,
	})
	if err != nil {
		return contract.Plan{}, fmt.Errorf("encode go-images process plan: %w", err)
	}
	normalizedJSON, err := json.Marshal(normalized)
	if err != nil {
		return contract.Plan{}, fmt.Errorf("encode normalized go-images input: %w", err)
	}
	parameters, err := PipelineParameters(normalized.Mode, normalized.SourceBuildID)
	if err != nil {
		return contract.Plan{}, err
	}
	return contract.Plan{
		VariantID: variantID,
		Test:      normalized.Mode == ModeTest, Input: normalizedJSON, Payload: payloadJSON,
		SessionID: document.ID,
		View:      goImagesPlanView(normalized, source, rollbackSource, parameters, len(steps), false),
		Target:    goImagesProcessTarget(),
	}, nil
}

func newRollbackInputSet(input *rollbackVariantInput) *contract.InputSet {
	inputs := contract.NewInputSet()
	inputs.PositiveIntVar(&input.SourceBuildID, "sourceBuildId", contract.FieldOptions{
		Label: "Source build ID", Placeholder: "3034159",
		Description: "The server verifies that this is a successful pipeline 1023 run which produced its own artifacts.",
	})
	return inputs
}

func goImagesPlanInput(selection contract.Selection) (PlanInput, error) {
	inputs := contract.NewInputSet()
	result := PlanInput{Mode: Mode(selection.VariantID)}
	if result.Mode == ModeRollback {
		var rollback rollbackVariantInput
		inputs = newRollbackInputSet(&rollback)
		if err := inputs.Parse(selection.Input); err != nil {
			return PlanInput{}, err
		}
		result.SourceBuildID = strconv.Itoa(rollback.SourceBuildID)
		return result, nil
	}
	if result.Mode != ModeNormal && result.Mode != ModeTest {
		return PlanInput{}, fmt.Errorf("unsupported go-images release variant %q", selection.VariantID)
	}
	if err := inputs.Parse(selection.Input); err != nil {
		return PlanInput{}, err
	}
	return result, nil
}

func (p *goImagesProcess) Restore(state *contract.State) (contract.Run, error) {
	if err := p.validate(state); err != nil {
		return nil, err
	}
	restored := releaseui.CloneReleaseRunState(state)
	if restored.VariantID == "" {
		input, err := decodeStrictJSON[PlanInput](restored.Input)
		if err != nil {
			return nil, err
		}
		restored.VariantID = string(input.Mode)
	}
	return &goImagesRun{process: p, state: restored}, nil
}

func (r *goImagesRun) Snapshot() *contract.State {
	return releaseui.CloneReleaseRunState(r.state)
}

func (r *goImagesRun) Steps(
	ctx context.Context,
	checkpoint contract.CheckpointFunc,
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
		state, err = decodeStrictJSON[State](run.Checkpoint)
		if err != nil {
			return nil, fmt.Errorf("decode go-images checkpoint: %w", err)
		}
	}
	var service RunService = disabledGoImagesService{}
	workflowCheckpoint := CheckpointFunc(nil)
	if checkpoint != nil && !run.Complete {
		if state.BuildID == "" {
			current, err := r.process.service.ResolveCurrentSource(ctx)
			if err != nil {
				return nil, fmt.Errorf("re-resolve current microsoft/main: %w", err)
			}
			if current.Branch != SourceBranch || current.Commit != payload.Document.Input.SourceVersion {
				return nil, errors.New("microsoft/main changed after this plan was prepared; refresh the plan before queueing")
			}
			if payload.Document.Input.Mode == ModeRollback {
				buildID, _ := strconv.Atoi(payload.Document.Input.SourceBuildID)
				if _, err := r.process.service.ValidateRollback(ctx, buildID); err != nil {
					return nil, fmt.Errorf("revalidate rollback source: %w", err)
				}
			}
		}
		service, err = r.process.service.NewRunService(RunRequest{
			Mode: payload.Document.Input.Mode, SessionID: payload.Document.ID, ExecutionDigest: run.Digest,
			Versions:      append([]string(nil), payload.Document.Input.Versions...),
			SourceBuildID: payload.Document.Input.SourceBuildID, SourceVersion: payload.Document.Input.SourceVersion,
			PreviousQueueAttempt: state.QueueAttempted,
		})
		if err != nil {
			return nil, fmt.Errorf("create go-images execution service: %w", err)
		}
		workflowCheckpoint = func(ctx context.Context, state *State) error {
			stateJSON, err := json.Marshal(state)
			if err != nil {
				return fmt.Errorf("encode go-images checkpoint: %w", err)
			}
			return checkpoint(ctx, contract.Checkpoint{
				State: stateJSON, External: goImagesExternalRun(state),
			})
		}
	}
	input := payload.Document.Input
	steps, _, err := NewGraphWithCheckpoint(&input, &state, service, workflowCheckpoint)
	if err != nil {
		return nil, err
	}
	if err := payload.Document.ValidateGraph(steps); err != nil {
		return nil, err
	}
	return steps, nil
}

func (p *goImagesProcess) validate(run *contract.State) error {
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
	if run.VariantID != "" && run.VariantID != string(input.Mode) {
		return errors.New("go-images process variant does not match its input")
	}
	if run.SessionID != payload.Document.ID {
		return errors.New("go-images process session ID does not match its document")
	}
	if run.Test != (input.Mode == ModeTest) {
		return errors.New("go-images process test classification is invalid")
	}
	if err := validateCurrentSource(payload.Source); err != nil {
		return err
	}
	normalizedSourceVersions, err := normalizeResolvedVersions(payload.Source.Versions)
	if err != nil || !reflect.DeepEqual(normalizedSourceVersions, payload.Source.Versions) ||
		payload.Source.Commit != payload.Document.Input.SourceVersion ||
		input.Mode != ModeRollback && !reflect.DeepEqual(payload.Source.Versions, payload.Document.Input.Versions) {

		return errors.New("go-images source does not match its document")
	}
	if err := validateGoImagesRollbackPayload(input, payload); err != nil {
		return err
	}
	initialState, err := NewState(&payload.Document.Input)
	if err != nil || *initialState != payload.Document.State {
		return errors.New("go-images payload document does not contain its initial state")
	}
	inputCopy := payload.Document.Input
	expectedSteps, _, err := NewGraphWithCheckpoint(
		&inputCopy, &payload.Document.State, disabledGoImagesService{}, nil,
	)
	if err != nil {
		return fmt.Errorf("validate go-images process graph: %w", err)
	}
	if err := payload.Document.ValidateGraph(expectedSteps); err != nil {
		return err
	}
	parameters, err := PipelineParameters(input.Mode, input.SourceBuildID)
	if err != nil {
		return err
	}
	if run.Target != goImagesProcessTarget() ||
		!reflect.DeepEqual(run.View, goImagesPlanView(input, payload.Source, payload.RollbackSource, parameters, len(expectedSteps), false)) {

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
	state, err := decodeStrictJSON[State](run.Checkpoint)
	if err != nil {
		return fmt.Errorf("decode go-images process checkpoint: %w", err)
	}
	checkpointInput := payload.Document.Input
	checkpointSteps, _, err := NewGraphWithCheckpoint(
		&checkpointInput, &state, disabledGoImagesService{}, nil,
	)
	if err != nil {
		return fmt.Errorf("validate go-images process checkpoint: %w", err)
	}
	if err := payload.Document.ValidateGraph(checkpointSteps); err != nil {
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
	if p.service == nil {
		return errors.New("go-images service is unavailable")
	}
	return nil
}

func validateGoImagesRollbackPayload(input PlanInput, payload goImagesProcessPayload) error {
	if input.Mode != ModeRollback {
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

func goImagesProcessTarget() contract.Reference {
	return contract.Reference{
		ID:        strconv.Itoa(DefinitionID),
		URL:       "https://dev.azure.com/dnceng/internal/_build?definitionId=1023",
		LinkLabel: "Open go-images pipeline 1023",
	}
}

func goImagesExternalRun(state *State) *contract.Reference {
	if state == nil || state.BuildID == "" {
		return nil
	}
	reference := &contract.Reference{
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

func equalProcessRunReference(left, right *contract.Reference) bool {
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
	_ contract.Process = (*goImagesProcess)(nil)
	_ contract.Run     = (*goImagesRun)(nil)
)
