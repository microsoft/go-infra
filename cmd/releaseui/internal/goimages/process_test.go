// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package goimages

import (
	"context"
	"errors"
	"testing"

	"github.com/microsoft/go-infra/releaseui/contract"
)

const processTestCommit = "0123456789abcdef0123456789abcdef01234567"

type fakeProcessService struct{}

type recordingProcessService struct {
	fakeProcessService
	request *RunRequest
}

type inspectingProcessService struct {
	fakeProcessService
	calls   int
	request RunRequest
	buildID string
}

func (s *inspectingProcessService) InspectRun(_ context.Context, request RunRequest, buildID string) (*contract.RunInspection, error) {
	s.calls++
	s.request, s.buildID = request, buildID
	return &contract.RunInspection{RunURL: pipelineRunURL(buildID), Status: "canceled"}, nil
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
	return disabledGoImagesService{}, nil
}

func (fakeProcessService) InspectRun(context.Context, RunRequest, string) (*contract.RunInspection, error) {
	return nil, errors.New("fake pipeline inspection is not configured")
}

func (service *recordingProcessService) NewRunService(request RunRequest) (RunService, error) {
	service.request = &request
	return disabledGoImagesService{}, nil
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
	if string(before.Input) != string(after.Input) || string(before.State) != string(after.State) {
		t.Fatal("Plan or Build changed the run state")
	}
	if view := run.TakeView(); view.Test || view.Summary != "Ready" {
		t.Fatalf("view = %#v", view)
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

func TestTestReleaseInspectionDoesNotChangeSavedState(t *testing.T) {
	service := &inspectingProcessService{}
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
	inspector, ok := run.(contract.RunInspector)
	if !ok {
		t.Fatal("test release has no read-only inspector")
	}
	inspection, err := inspector.Inspect(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	after := run.TakeSnapshot()
	if inspection.Status != "canceled" || inspection.RunURL != pipelineRunURL("888") ||
		service.calls != 1 || service.buildID != "888" || service.request.Mode != ModeTest ||
		service.request.SessionID != state.Document.ID || service.request.ExecutionDigest != state.Document.ExecutionDigest {

		t.Fatalf("inspection = %#v, service = %#v", inspection, service)
	}
	if string(before.Input) != string(after.Input) || string(before.State) != string(after.State) {
		t.Fatal("read-only inspection changed the saved run state")
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
