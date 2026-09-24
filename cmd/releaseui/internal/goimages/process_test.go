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

var _ contract.ProcessGroup = NewProcess(fakeProcessService{})
