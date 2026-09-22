// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package goimages

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/microsoft/go-infra/releaseui/coordinator"
)

const workflowTestCommit = "81ce9afc2b75ec4e153dd15fc3c7539b12024945"

var testInput = &Input{
	Versions: []string{"1.25.12-1", "1.26.5-2"}, Mode: ModeNormal,
	SourceVersion: workflowTestCommit,
}

type fakeService struct {
	mirrorErr error
	pollErr   error
	mirrors   int
	queues    int
	polls     int
}

func (service *fakeService) PollMirror(_ context.Context, commit string) error {
	service.mirrors++
	if commit != workflowTestCommit {
		return errors.New("unexpected mirror commit")
	}
	return service.mirrorErr
}

func (service *fakeService) QueuePipeline(_ context.Context, parameters map[string]string) (string, error) {
	service.queues++
	if parameters["publishRepoPrefix"] != "public/" {
		return "", errors.New("unexpected queue request")
	}
	return "888", nil
}

func (service *fakeService) PollPipeline(_ context.Context, buildID string) error {
	service.polls++
	if buildID != "888" {
		return errors.New("unexpected build ID")
	}
	return service.pollErr
}

func TestPipelineParameters(t *testing.T) {
	for _, test := range []struct {
		mode          Mode
		sourceBuildID string
		wantSource    string
		wantPrefix    string
	}{
		{mode: ModeNormal, wantSource: "$(Build.BuildId)", wantPrefix: "public/"},
		{mode: ModeRollback, sourceBuildID: "3019035", wantSource: "3019035", wantPrefix: "public/"},
		{mode: ModeTest, wantSource: "$(Build.BuildId)", wantPrefix: "dev/"},
	} {
		t.Run(string(test.mode), func(t *testing.T) {
			parameters, err := PipelineParameters(test.mode, test.sourceBuildID)
			if err != nil {
				t.Fatal(err)
			}
			if len(parameters) != 2 || parameters["sourceBuildPipelineRunId"] != test.wantSource ||
				parameters["publishRepoPrefix"] != test.wantPrefix {

				t.Fatalf("parameters = %#v", parameters)
			}
		})
	}
	if _, err := PipelineParameters(ModeNormal, "123"); err == nil {
		t.Fatal("normal release accepted a source build")
	}
	if _, err := PipelineParameters(ModeRollback, "invalid"); err == nil {
		t.Fatal("rollback accepted an invalid source build")
	}
}

func TestGraphCheckpointsQueueAndCompletion(t *testing.T) {
	service := &fakeService{}
	var checkpoints []State
	steps, state, err := NewGraphWithCheckpoint(testInput, nil, service, func(state *State) {
		checkpoints = append(checkpoints, *state)
	})
	if err != nil {
		t.Fatal(err)
	}
	var runner coordinator.StepRunner
	if err := runner.Execute(context.Background(), steps); err != nil {
		t.Fatal(err)
	}
	if service.mirrors != 1 || service.queues != 1 || service.polls != 1 {
		t.Fatalf("service calls = mirror %d, queue %d, poll %d", service.mirrors, service.queues, service.polls)
	}
	if state.BuildID != "888" || !state.Complete || state.Result != "succeeded" {
		t.Fatalf("state = %#v", state)
	}
	if len(checkpoints) != 4 || checkpoints[0].VerifiedMirroredCommit != workflowTestCommit ||
		checkpoints[0].QueueAttempted || !checkpoints[1].QueueAttempted || checkpoints[1].BuildID != "" ||
		checkpoints[2].BuildID != "888" || checkpoints[2].Complete || !checkpoints[3].Complete {

		t.Fatalf("checkpoints = %#v", checkpoints)
	}
}

func TestGraphCheckpointsTerminalPipelineResult(t *testing.T) {
	for _, result := range []string{"failed", "canceled"} {
		t.Run(result, func(t *testing.T) {
			terminalErr := errors.New("pipeline " + result)
			service := &fakeService{pollErr: &PipelineResultError{Result: result, Err: terminalErr}}
			var checkpoint State
			steps, state, err := NewGraphWithCheckpoint(testInput, nil, service, func(state *State) {
				checkpoint = *state
			})
			if err != nil {
				t.Fatal(err)
			}
			var runner coordinator.StepRunner
			if err := runner.Execute(context.Background(), steps); !errors.Is(err, terminalErr) {
				t.Fatalf("error = %v, want %v", err, terminalErr)
			}
			if !state.Complete || state.Result != result || !checkpoint.Complete || checkpoint.Result != result {
				t.Fatalf("state = %#v, checkpoint = %#v", state, checkpoint)
			}
		})
	}
}

func TestGraphLeavesTransientPollFailureIncomplete(t *testing.T) {
	pollErr := errors.New("pipeline read unavailable")
	service := &fakeService{pollErr: pollErr}
	steps, state, err := NewGraphWithCheckpoint(testInput, nil, service, func(*State) {})
	if err != nil {
		t.Fatal(err)
	}
	var runner coordinator.StepRunner
	if err := runner.Execute(context.Background(), steps); !errors.Is(err, pollErr) {
		t.Fatalf("error = %v, want %v", err, pollErr)
	}
	if state.Complete || state.Result != "" {
		t.Fatalf("state = %#v, want resumable incomplete state", state)
	}
}

func TestGraphBlocksQueueUntilMirrorAvailable(t *testing.T) {
	mirrorErr := errors.New("not mirrored")
	service := &fakeService{mirrorErr: mirrorErr}
	steps, _, err := NewGraphWithCheckpoint(testInput, nil, service, nil)
	if err != nil {
		t.Fatal(err)
	}
	var runner coordinator.StepRunner
	if err := runner.Execute(context.Background(), steps); !errors.Is(err, mirrorErr) {
		t.Fatalf("error = %v, want %v", err, mirrorErr)
	}
	if service.queues != 0 {
		t.Fatal("pipeline was queued before mirror verification")
	}
}

func TestGraphResumesKnownBuildWithoutQueue(t *testing.T) {
	state, err := NewState(testInput)
	if err != nil {
		t.Fatal(err)
	}
	state.QueueAttempted = true
	state.BuildID = "888"
	state.VerifiedMirroredCommit = workflowTestCommit
	service := &fakeService{}
	steps, state, err := NewGraphWithCheckpoint(testInput, state, service, nil)
	if err != nil {
		t.Fatal(err)
	}
	var runner coordinator.StepRunner
	if err := runner.Execute(context.Background(), steps); err != nil {
		t.Fatal(err)
	}
	if service.mirrors != 0 || service.queues != 0 || service.polls != 1 || !state.Complete {
		t.Fatalf("service = %#v, state = %#v", service, state)
	}
}

func TestGraphRejectsKnownBuildWithoutMirrorCheckpoint(t *testing.T) {
	state, err := NewState(testInput)
	if err != nil {
		t.Fatal(err)
	}
	state.QueueAttempted = true
	state.BuildID = "888"
	if _, _, err := NewGraphWithCheckpoint(testInput, state, &fakeService{}, nil); err == nil {
		t.Fatal("state with a build but no mirror checkpoint unexpectedly passed validation")
	}
}

func TestValidateState(t *testing.T) {
	initial, err := NewState(testInput)
	if err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		name  string
		state State
	}{
		{name: "checksum", state: State{InputChecksum: initial.InputChecksum + 1}},
		{name: "wrong mirrored commit", state: State{InputChecksum: initial.InputChecksum, VerifiedMirroredCommit: "0123456789abcdef0123456789abcdef01234567"}},
		{name: "invalid build", state: State{InputChecksum: initial.InputChecksum, QueueAttempted: true, BuildID: "invalid"}},
		{name: "build before queue", state: State{InputChecksum: initial.InputChecksum, BuildID: "888"}},
		{name: "incomplete result", state: State{InputChecksum: initial.InputChecksum, QueueAttempted: true, Result: "failed"}},
		{name: "complete before queue", state: State{InputChecksum: initial.InputChecksum, Complete: true, Result: "uncertain"}},
		{name: "complete without result", state: State{InputChecksum: initial.InputChecksum, QueueAttempted: true, Complete: true}},
		{name: "success without build", state: State{InputChecksum: initial.InputChecksum, QueueAttempted: true, Complete: true, Result: "succeeded"}},
	} {
		t.Run(test.name, func(t *testing.T) {
			if err := ValidateState(testInput, &test.state); err == nil {
				t.Fatalf("invalid state unexpectedly passed validation: %#v", test.state)
			}
		})
	}
	uncertain := &State{
		InputChecksum:          initial.InputChecksum,
		VerifiedMirroredCommit: testInput.SourceVersion,
		QueueAttempted:         true,
		Complete:               true,
		Result:                 "uncertain",
	}
	if err := ValidateState(testInput, uncertain); err != nil {
		t.Fatalf("valid uncertain repair failed validation: %v", err)
	}
}

func TestStateAccessReportsChangeAndObservesCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	checkpointCalls := 0
	state := &State{}
	access := &stateAccess{
		state: state,
		checkpoint: func(snapshot *State) {
			checkpointCalls++
			if !snapshot.QueueAttempted {
				t.Error("checkpoint did not receive updated state")
			}
			cancel()
		},
	}
	if err := access.update(ctx, func() {
		state.QueueAttempted = true
	}); !errors.Is(err, context.Canceled) {
		t.Fatalf("update error = %v, want cancellation", err)
	}
	if checkpointCalls != 1 || !state.QueueAttempted {
		t.Fatalf("checkpoint calls = %d, state = %#v", checkpointCalls, state)
	}
}

func TestStateAccessSnapshotWaitsForUpdate(t *testing.T) {
	state := &State{}
	access := &stateAccess{state: state}
	updateStarted := make(chan struct{})
	finishUpdate := make(chan struct{})
	updateDone := make(chan error, 1)
	go func() {
		updateDone <- access.update(context.Background(), func() {
			close(updateStarted)
			<-finishUpdate
			state.QueueAttempted = true
		})
	}()
	<-updateStarted

	snapshotDone := make(chan State, 1)
	go func() {
		snapshotDone <- access.snapshot()
	}()
	select {
	case snapshot := <-snapshotDone:
		t.Fatalf("snapshot completed during update: %#v", snapshot)
	case <-time.After(10 * time.Millisecond):
	}
	close(finishUpdate)
	if err := <-updateDone; err != nil {
		t.Fatal(err)
	}
	if snapshot := <-snapshotDone; !snapshot.QueueAttempted {
		t.Fatalf("snapshot = %#v, want completed update", snapshot)
	}
}
