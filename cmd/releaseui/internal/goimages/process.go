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
	"sync"
	"time"

	"github.com/microsoft/go-infra/releaseui/contract"
	"github.com/microsoft/go-infra/releaseui/coordinator"
	"github.com/microsoft/go-infra/releaseui/releaseflag"
)

const (
	goImagesPipelineName    = "microsoft-go-images (official)"
	goImagesPipelineOrg     = "dnceng"
	goImagesPipelineProject = "internal"
	goImagesStateSchema     = 1
)

// PlanInput is the browser-controlled input for a go-images release.
type PlanInput struct {
	Mode          Mode   `json:"mode"`
	SourceBuildID string `json:"sourceBuildId,omitempty"`
}

type rollbackInput struct {
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

type goImagesProcessGroup struct {
	service ProcessService
}

type goImagesProcessBase struct {
	service ProcessService
}

type goImagesSnapshotState struct {
	SchemaVersion  int             `json:"schemaVersion"`
	Document       Document        `json:"document"`
	Source         Source          `json:"source"`
	RollbackSource *RollbackSource `json:"rollbackSource,omitempty"`
	Workflow       State           `json:"workflow"`
	UpdatedAt      time.Time       `json:"updatedAt"`
}

type goImagesRun struct {
	mu      sync.RWMutex
	service ProcessService
	input   PlanInput
	state   goImagesSnapshotState
}

type normalProcess struct{ goImagesProcessBase }
type rollbackProcess struct{ goImagesProcessBase }
type testProcess struct{ goImagesProcessBase }

// NewProcess creates the Go-images release process group.
func NewProcess(service ProcessService) contract.ProcessGroup {
	return &goImagesProcessGroup{service: service}
}

func (g *goImagesProcessGroup) Processes() []contract.Process {
	return []contract.Process{
		&normalProcess{goImagesProcessBase{service: g.service}},
		&rollbackProcess{goImagesProcessBase{service: g.service}},
		&testProcess{goImagesProcessBase{service: g.service}},
	}
}

func (g *goImagesProcessGroup) ProcessGroupIdentity() *contract.Identity {
	return &contract.Identity{
		Name: "Go images", Mark: "GI",
		Description:      "Build, sign, publish, test, or republish the Microsoft Build of Go container images.",
		DocumentationURL: "https://github.com/microsoft/go-lab/tree/main/docs/release#golang-toolset-images",
	}
}

func (p *normalProcess) Definition() contract.ProcessDefinition {
	return contract.ProcessDefinition{
		Identity: contract.Identity{
			Name:        "Normal release",
			Description: "Build current microsoft/main and publish to public/. All parameters are locked.",
		},
		ID:               "go-images-normal",
		InputPreamble:    "Choose a Go-images release process and review its fixed Azure DevOps request.",
		InputSubmitLabel: "Prepare release",
		Notice: &contract.Notice{
			Title:   "Normal release is locked.",
			Message: "Current main, current-build artifacts, and public/ are selected server-side.",
		},
	}
}

func (p *rollbackProcess) Definition() contract.ProcessDefinition {
	return contract.ProcessDefinition{
		Identity: contract.Identity{
			Name:        "Rollback / republish",
			Description: "Republish artifacts from one successful pipeline 1023 build to public/.",
		},
		ID:               "go-images-rollback",
		InputPreamble:    "Choose a Go-images release process and review its fixed Azure DevOps request.",
		InputSubmitLabel: "Prepare release",
		Notice: &contract.Notice{
			Title:   "Only the source build is editable.",
			Message: "The server locks current main and public/, then validates the selected build.",
		},
	}
}

func (p *testProcess) Definition() contract.ProcessDefinition {
	return contract.ProcessDefinition{
		Identity: contract.Identity{
			Name:        "Test release",
			Description: "Build current microsoft/main and publish only under the dev/ prefix.",
		},
		ID:               "go-images-test",
		InputPreamble:    "Choose a Go-images release process and review its fixed Azure DevOps request.",
		InputSubmitLabel: "Prepare release",
		Notice: &contract.Notice{
			Title:   "Test release is locked to dev/.",
			Message: "Current main is built normally, but publication is isolated under the dev/ prefix.",
		},
	}
}

func (p goImagesProcessBase) Preflight(ctx context.Context) (warning error, blocking error) {
	if err := validateGoImagesConfiguration(p.service); err != nil {
		return nil, err
	}
	_, err := p.service.Preflight(ctx)
	return nil, err
}

func (p *normalProcess) InputForm(*releaseflag.InputSet) any { return nil }
func (p *testProcess) InputForm(*releaseflag.InputSet) any   { return nil }

func (p *rollbackProcess) InputForm(inputs *releaseflag.InputSet) any {
	result := new(rollbackInput)
	inputs.PositiveIntVar(&result.SourceBuildID, "sourceBuildId", releaseflag.FieldOptions{
		Label: "Source build ID", Placeholder: "3034159",
		Description: "The server verifies that this is a successful pipeline 1023 run which produced its own artifacts.",
	})
	return result
}

func (p *normalProcess) Prepare(ctx context.Context, inputFormResult any) (*contract.StateSnapshot, error) {
	if inputFormResult != nil {
		panic("go-images release without inputs received an input value")
	}
	return prepareGoImages(ctx, p.service, PlanInput{Mode: ModeNormal})
}

func (p *testProcess) Prepare(ctx context.Context, inputFormResult any) (*contract.StateSnapshot, error) {
	if inputFormResult != nil {
		panic("go-images release without inputs received an input value")
	}
	return prepareGoImages(ctx, p.service, PlanInput{Mode: ModeTest})
}

func (p *rollbackProcess) Prepare(ctx context.Context, inputFormResult any) (*contract.StateSnapshot, error) {
	input, ok := inputFormResult.(*rollbackInput)
	if !ok || input == nil {
		panic("go-images rollback received an unexpected input type")
	}
	return prepareGoImages(ctx, p.service, PlanInput{
		Mode: ModeRollback, SourceBuildID: strconv.Itoa(input.SourceBuildID),
	})
}

func prepareGoImages(
	ctx context.Context,
	service ProcessService,
	input PlanInput,
) (*contract.StateSnapshot, error) {
	if err := validateGoImagesConfiguration(service); err != nil {
		return nil, err
	}
	normalized, err := normalizePlanInput(input)
	if err != nil {
		return nil, errors.Join(err, contract.ErrInvalidInput)
	}
	if _, err := service.Preflight(ctx); err != nil {
		return nil, fmt.Errorf("azure preflight failed: %w", err)
	}
	source, err := service.ResolveCurrentSource(ctx)
	if err != nil {
		return nil, fmt.Errorf("resolve current microsoft/main: %w", err)
	}
	if err := validateCurrentSource(source); err != nil {
		return nil, err
	}
	source.Versions, err = normalizeResolvedVersions(source.Versions)
	if err != nil {
		return nil, err
	}
	versions := append([]string(nil), source.Versions...)
	var rollbackSource *RollbackSource
	if normalized.Mode == ModeRollback {
		buildID, _ := strconv.Atoi(normalized.SourceBuildID)
		validated, err := service.ValidateRollback(ctx, buildID)
		if err != nil {
			return nil, fmt.Errorf("validate rollback source: %w", err)
		}
		if validated.BuildID != buildID {
			return nil, errors.New("rollback validation returned a different build")
		}
		validated.Versions, err = normalizeResolvedVersions(validated.Versions)
		if err != nil {
			return nil, err
		}
		rollbackSource = &validated
		versions = append([]string(nil), validated.Versions...)
	}
	workflowInput := &Input{
		Versions: versions, Mode: normalized.Mode, SourceVersion: source.Commit,
		SourceBuildID: normalized.SourceBuildID,
	}
	_, workflowState, err := NewGraphWithCheckpoint(workflowInput, nil, disabledGoImagesService{}, nil)
	if err != nil {
		return nil, fmt.Errorf("create go-images plan: %w", err)
	}
	document, err := NewDocument(workflowInput, workflowState, time.Now())
	if err != nil {
		return nil, fmt.Errorf("create durable release session: %w", err)
	}
	state := goImagesSnapshotState{
		SchemaVersion: goImagesStateSchema,
		Document:      *document, Source: source, RollbackSource: rollbackSource,
		Workflow: *workflowState, UpdatedAt: time.Now().UTC(),
	}
	return encodeGoImagesSnapshot(normalized, state)
}

func loadGoImagesRun(
	service ProcessService,
	mode Mode,
	snapshot *contract.StateSnapshot,
) (contract.Run, error) {
	input, state, err := decodeGoImagesSnapshot(snapshot)
	if err != nil {
		return nil, err
	}
	if input.Mode != mode {
		return nil, errors.New("go-images snapshot belongs to a different process")
	}
	if err := validateGoImagesState(input, state); err != nil {
		return nil, err
	}
	return &goImagesRun{service: service, input: input, state: state}, nil
}

func (p *normalProcess) Load(snapshot *contract.StateSnapshot) (contract.Run, error) {
	return loadGoImagesRun(p.service, ModeNormal, snapshot)
}

func (p *rollbackProcess) Load(snapshot *contract.StateSnapshot) (contract.Run, error) {
	return loadGoImagesRun(p.service, ModeRollback, snapshot)
}

func (p *testProcess) Load(snapshot *contract.StateSnapshot) (contract.Run, error) {
	return loadGoImagesRun(p.service, ModeTest, snapshot)
}

func (r *goImagesRun) TakeSnapshot() *contract.StateSnapshot {
	r.mu.RLock()
	defer r.mu.RUnlock()
	snapshot, err := encodeGoImagesSnapshot(r.input, r.state)
	if err != nil {
		panic(fmt.Sprintf("encode go-images snapshot: %v", err))
	}
	return snapshot
}

func (r *goImagesRun) TakeView() *contract.RunView {
	r.mu.RLock()
	defer r.mu.RUnlock()
	view := &contract.RunView{
		Test: r.input.Mode == ModeTest, Summary: "Ready",
		Detail: modeName(r.input.Mode), UpdatedAt: r.state.UpdatedAt,
	}
	if r.state.Workflow.BuildID != "" {
		view.Summary = "Azure DevOps pipeline is running"
		view.Detail = "Build " + r.state.Workflow.BuildID
	}
	if r.state.Workflow.Complete {
		view.Summary = "Azure DevOps pipeline completed"
		view.Detail = "Build " + r.state.Workflow.BuildID + " finished with result " + r.state.Workflow.Result
		view.Completed, view.Total = 1, 1
	}
	return view
}

func (r *goImagesRun) Plan() *contract.Plan {
	r.mu.RLock()
	defer r.mu.RUnlock()
	parameters, err := PipelineParameters(r.input.Mode, r.input.SourceBuildID)
	if err != nil {
		panic(err)
	}
	input := r.state.Document.Input
	steps, _, err := NewGraphWithCheckpoint(&input, &r.state.Workflow, disabledGoImagesService{}, nil)
	if err != nil {
		panic(err)
	}
	return goImagesPlan(r.input, r.state.Source, r.state.RollbackSource, parameters, len(steps))
}

func (r *goImagesRun) Build(ctx context.Context, checkpoint contract.CheckpointFunc) ([]*coordinator.Step, error) {
	if err := validateGoImagesConfiguration(r.service); err != nil {
		return nil, err
	}
	r.mu.RLock()
	input := r.state.Document.Input
	state := r.state.Workflow
	source := r.state.Source
	sessionID := r.state.Document.ID
	r.mu.RUnlock()

	var service RunService = disabledGoImagesService{}
	workflowCheckpoint := CheckpointFunc(nil)
	if checkpoint != nil {
		if state.BuildID == "" {
			current, err := r.service.ResolveCurrentSource(ctx)
			if err != nil {
				return nil, fmt.Errorf("re-resolve current microsoft/main: %w", err)
			}
			if current.Branch != SourceBranch || current.Commit != input.SourceVersion {
				return nil, errors.New("microsoft/main changed after this plan was prepared; refresh the plan before queueing")
			}
			if input.Mode == ModeRollback {
				buildID, _ := strconv.Atoi(input.SourceBuildID)
				if _, err := r.service.ValidateRollback(ctx, buildID); err != nil {
					return nil, fmt.Errorf("revalidate rollback source: %w", err)
				}
			}
		}
		var err error
		service, err = r.service.NewRunService(RunRequest{
			Mode: input.Mode, SessionID: sessionID, ExecutionDigest: sessionID,
			Versions: append([]string(nil), input.Versions...), SourceBuildID: input.SourceBuildID,
			SourceVersion: source.Commit, PreviousQueueAttempt: state.QueueAttempted,
		})
		if err != nil {
			return nil, fmt.Errorf("create go-images execution service: %w", err)
		}
		workflowCheckpoint = func(state *State) {
			r.mu.Lock()
			r.state.Workflow = *state
			r.state.UpdatedAt = time.Now().UTC()
			r.mu.Unlock()
			checkpoint()
		}
	}
	steps, _, err := NewGraphWithCheckpoint(&input, &state, service, workflowCheckpoint)
	return steps, err
}

func encodeGoImagesSnapshot(input PlanInput, state goImagesSnapshotState) (*contract.StateSnapshot, error) {
	inputJSON, err := json.Marshal(input)
	if err != nil {
		return nil, fmt.Errorf("encode go-images input: %w", err)
	}
	stateJSON, err := json.Marshal(state)
	if err != nil {
		return nil, fmt.Errorf("encode go-images state: %w", err)
	}
	return &contract.StateSnapshot{Input: inputJSON, State: stateJSON}, nil
}

func decodeGoImagesSnapshot(snapshot *contract.StateSnapshot) (PlanInput, goImagesSnapshotState, error) {
	if snapshot == nil {
		return PlanInput{}, goImagesSnapshotState{}, errors.New("go-images snapshot is nil")
	}
	input, err := decodeStrictJSON[PlanInput](snapshot.Input)
	if err != nil {
		return PlanInput{}, goImagesSnapshotState{}, fmt.Errorf("decode go-images input: %w", err)
	}
	state, err := decodeStrictJSON[goImagesSnapshotState](snapshot.State)
	if err != nil {
		return PlanInput{}, goImagesSnapshotState{}, fmt.Errorf("decode go-images state: %w", err)
	}
	if state.SchemaVersion != goImagesStateSchema {
		return PlanInput{}, goImagesSnapshotState{}, fmt.Errorf("unsupported go-images state schema %d", state.SchemaVersion)
	}
	return input, state, nil
}

func validateGoImagesState(input PlanInput, state goImagesSnapshotState) error {
	normalized, err := normalizePlanInput(input)
	if err != nil || normalized != input {
		return errors.New("go-images process input is invalid")
	}
	if err := state.Document.Validate(); err != nil {
		return fmt.Errorf("validate go-images document: %w", err)
	}
	if err := validateCurrentSource(state.Source); err != nil {
		return err
	}
	if input.Mode != state.Document.Input.Mode || input.SourceBuildID != state.Document.Input.SourceBuildID {
		return errors.New("go-images input does not match its document")
	}
	if err := validateGoImagesRollbackState(input, state); err != nil {
		return err
	}
	if err := ValidateState(&state.Document.Input, &state.Workflow); err != nil {
		return fmt.Errorf("validate go-images workflow state: %w", err)
	}
	return nil
}

func validateGoImagesRollbackState(input PlanInput, state goImagesSnapshotState) error {
	if input.Mode != ModeRollback {
		if state.RollbackSource != nil {
			return errors.New("non-rollback go-images process has a rollback source")
		}
		return nil
	}
	buildID, _ := strconv.Atoi(input.SourceBuildID)
	if state.RollbackSource == nil || state.RollbackSource.BuildID != buildID ||
		!strings.HasPrefix(state.RollbackSource.URL, "https://") ||
		!reflect.DeepEqual(state.RollbackSource.Versions, state.Document.Input.Versions) {

		return errors.New("go-images rollback source does not match its document")
	}
	return nil
}

func validateGoImagesConfiguration(service ProcessService) error {
	if service == nil {
		return errors.New("go-images service is unavailable")
	}
	return nil
}

func modeName(mode Mode) string {
	switch mode {
	case ModeNormal:
		return "Normal release"
	case ModeRollback:
		return "Rollback / republish"
	case ModeTest:
		return "Test release"
	default:
		return string(mode)
	}
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
	_ contract.ProcessGroup = (*goImagesProcessGroup)(nil)
	_ contract.Process      = (*normalProcess)(nil)
	_ contract.Process      = (*rollbackProcess)(nil)
	_ contract.Process      = (*testProcess)(nil)
	_ contract.Run          = (*goImagesRun)(nil)
)
