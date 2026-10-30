// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package goimages

import (
	"context"
	"errors"
	"testing"

	"github.com/microsoft/go-infra/releaseui/contract"
	"github.com/microsoft/go-infra/releaseui/coordinator"
)

const processTestCommit = "0123456789abcdef0123456789abcdef01234567"

type fakeProcessService struct{}

type recordingProcessService struct {
	fakeProcessService
	request *RunRequest
}

type observingProcessService struct {
	fakeProcessService
	fakeService
	request RunRequest
}

func (s *observingProcessService) NewRunService(request RunRequest) (RunService, error) {
	s.request = request
	return &s.fakeService, nil
}

func (fakeProcessService) Preflight(context.Context) (string, error) {
	return "ready", nil
}

func (fakeProcessService) ResolveCurrentSource(context.Context) (Source, error) {
	return Source{
		Branch: SourceBranch, Commit: processTestCommit,
		Versions: []string{"1.25.1-1"},
	}, nil
}

func (fakeProcessService) ValidateRollback(context.Context, int) (RollbackSource, error) {
	return RollbackSource{}, errors.New("unexpected rollback validation")
}

func (fakeProcessService) NewRunService(RunRequest) (RunService, error) {
	return &fakeService{}, nil
}

func (service *recordingProcessService) NewRunService(request RunRequest) (RunService, error) {
	service.request = &request
	return &fakeService{}, nil
}

func TestProcessGroupBuildsConcreteNormalRelease(t *testing.T) {
	group := NewProcess(fakeProcessService{})
	processes := group.Processes()
	if len(processes) != 3 || processes[0].Definition().ID != "go-images-normal" {
		t.Fatalf("processes = %#v", processes)
	}
	process := processes[0]
	snapshot, err := process.Prepare(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	run, err := process.Load(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	before := run.TakeSnapshot()
	plan := run.Plan()
	steps, err := run.Build(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	after := run.TakeSnapshot()
	if plan == nil || plan.ExecutionButtonLabel != "Run production release" || len(steps) == 0 {
		t.Fatalf("plan = %#v, steps = %d", plan, len(steps))
	}
	runner, err := coordinator.NewStepRunner(steps, coordinator.Snapshot{})
	if err != nil {
		t.Fatal(err)
	}
	if err := runner.Observe(context.Background()); err != nil {
		t.Fatalf("an unstarted graph tried to read an unknown build: %v", err)
	}
	if string(before.Input) != string(after.Input) || string(before.State) != string(after.State) {
		t.Fatal("Plan or Build changed the run state")
	}
	if view := run.TakeView(); view.Test || view.Summary != "Ready" {
		t.Fatalf("view = %#v", view)
	}
}

func TestPrepareAndPlanOnlyInitializeDomainState(t *testing.T) {
	for _, test := range []struct {
		mode  Mode
		index int
	}{
		{mode: ModeNormal, index: 0},
		{mode: ModeTest, index: 2},
	} {
		t.Run(string(test.mode), func(t *testing.T) {
			service := new(recordingProcessService)
			process := NewProcess(service).Processes()[test.index]
			snapshot, err := process.Prepare(context.Background(), nil)
			if err != nil {
				t.Fatal(err)
			}
			_, state, err := decodeGoImagesSnapshot(snapshot)
			if err != nil {
				t.Fatal(err)
			}
			initial, err := NewState(&state.Document.Input)
			if err != nil {
				t.Fatal(err)
			}
			if state.Workflow != *initial {
				t.Fatalf("preparation changed the initial workflow state: %#v", state.Workflow)
			}
			run, err := process.Load(snapshot)
			if err != nil {
				t.Fatal(err)
			}
			before := run.TakeSnapshot()
			plan := run.Plan()
			if plan == nil || plan.Subtitle == "" || service.request != nil {
				t.Fatalf("planning constructed an execution service or lost its description: %#v", plan)
			}
			after := run.TakeSnapshot()
			if string(before.Input) != string(after.Input) || string(before.State) != string(after.State) {
				t.Fatal("planning changed the process checkpoint")
			}
			steps, err := run.Build(context.Background(), nil)
			if err != nil {
				t.Fatal(err)
			}
			if service.request == nil || len(steps) != 3 {
				t.Fatalf("the graph builder did not construct the expected graph: %d steps", len(steps))
			}
		})
	}
}

func TestProcessBuildUsesDocumentExecutionIdentity(t *testing.T) {
	service := new(recordingProcessService)
	process := NewProcess(service).Processes()[0]
	snapshot, err := process.Prepare(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	_, state, err := decodeGoImagesSnapshot(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	run, err := process.Load(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := run.Build(context.Background(), func() {}); err != nil {
		t.Fatal(err)
	}
	if service.request == nil || service.request.SessionID != state.Document.ID ||
		service.request.ExecutionDigest != state.Document.ExecutionDigest ||
		!digestPattern.MatchString(service.request.ExecutionDigest) {

		t.Fatalf("run request = %#v, document = %#v", service.request, state.Document)
	}
}

func TestTestReleaseGraphObservationDoesNotChangeSavedState(t *testing.T) {
	service := &observingProcessService{fakeService: fakeService{status: coordinator.StepStatusCanceled}}
	process := NewProcess(service).Processes()[2]
	snapshot, err := process.Prepare(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	input, state, err := decodeGoImagesSnapshot(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	state.Workflow.QueueAttempted = true
	state.Workflow.VerifiedMirroredCommit = state.Document.Input.SourceVersion
	state.Workflow.BuildID = "888"
	snapshot, err = encodeGoImagesSnapshot(input, state)
	if err != nil {
		t.Fatal(err)
	}
	run, err := process.Load(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	before := run.TakeSnapshot()
	steps, err := run.Build(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	runner, err := coordinator.NewStepRunner(steps, coordinator.Snapshot{})
	if err != nil {
		t.Fatal(err)
	}
	if err := runner.Observe(context.Background()); err != nil {
		t.Fatal(err)
	}
	observation := runner.Snapshot().Steps[2]
	after := run.TakeSnapshot()
	if observation.Status != coordinator.StepStatusCanceled || observation.Progress.RunURL != pipelineRunURL("888") ||
		service.polls != 1 || service.mirrors != 0 || service.queues != 0 || service.request.Mode != ModeTest ||
		service.request.SessionID != state.Document.ID || service.request.ExecutionDigest != state.Document.ExecutionDigest {

		t.Fatalf("observation = %#v, service = %#v", observation, service)
	}
	if string(before.Input) != string(after.Input) || string(before.State) != string(after.State) {
		t.Fatal("read-only graph observation changed the saved run state")
	}
}

func TestTestReleaseViewLinksToItsPipelineRun(t *testing.T) {
	process := NewProcess(fakeProcessService{}).Processes()[2]
	snapshot, err := process.Prepare(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	input, state, err := decodeGoImagesSnapshot(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	run, err := process.Load(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	if run.TakeView().RunURL != "" {
		t.Fatal("a release without a build ID has a pipeline link")
	}
	state.Workflow.VerifiedMirroredCommit = state.Document.Input.SourceVersion
	state.Workflow.QueueAttempted = true
	state.Workflow.BuildID = "888"
	for _, complete := range []bool{false, true} {
		state.Workflow.Complete = complete
		if complete {
			state.Workflow.Result = "succeeded"
		}
		snapshot, err := encodeGoImagesSnapshot(input, state)
		if err != nil {
			t.Fatal(err)
		}
		run, err := process.Load(snapshot)
		if err != nil {
			t.Fatal(err)
		}
		view := run.TakeView()
		if !view.Test || view.RunURL != "https://dev.azure.com/dnceng/internal/_build/results?buildId=888" {
			t.Fatalf("test release view = %#v", view)
		}
	}
}

var _ contract.ProcessGroup = NewProcess(fakeProcessService{})
