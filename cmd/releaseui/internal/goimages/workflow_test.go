// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package goimages

import (
	"context"
	"errors"
	"strings"
	"testing"

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
	status    coordinator.StepStatus
	mirrors   int
	queues    int
	polls     int
}

func (service *fakeService) PollMirror(_ context.Context, _ coordinator.StepControl, commit string) error {
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

func (service *fakeService) PollPipeline(_ context.Context, _ coordinator.StepControl, buildID string) error {
	service.polls++
	if buildID != "888" {
		return errors.New("unexpected build ID")
	}
	if service.pollErr != nil {
		return service.pollErr
	}
	status := service.status
	if status == "" {
		status = coordinator.StepStatusSucceeded
	}
	if status == coordinator.StepStatusFailed || status == coordinator.StepStatusCanceled {
		return &PipelineResultError{Result: string(status), Err: errors.New("pipeline " + string(status))}
	}
	return nil
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
	run := newWorkflowTestRun(t, testInput, nil, service)
	var checkpoints []State
	steps, err := run.Build()
	if err != nil {
		t.Fatal(err)
	}
	var runner coordinator.StepRunner
	if err := runner.ExecuteWithCallbacks(context.Background(), steps, coordinator.ExecutionCallbacks{
		Capture: func() error {
			saved, err := run.TakeSnapshot()
			if err != nil {
				return err
			}
			_, stored, err := decodeGoImagesSnapshot(saved)
			if err != nil {
				return err
			}
			checkpoints = append(checkpoints, stored.Workflow)
			return validateGoImagesSnapshot(ModeNormal, saved)
		},
		Persist: func() error { return nil },
	}); err != nil {
		t.Fatal(err)
	}
	state := run.state.Workflow
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
			service := &fakeService{status: coordinator.StepStatus(result)}
			run := newWorkflowTestRun(t, testInput, nil, service)
			var checkpoint State
			steps, err := run.Build()
			if err != nil {
				t.Fatal(err)
			}
			var runner coordinator.StepRunner
			err = runner.ExecuteWithCallbacks(context.Background(), steps, coordinator.ExecutionCallbacks{
				Capture: func() error {
					saved, err := run.TakeSnapshot()
					if err != nil {
						return err
					}
					_, stored, err := decodeGoImagesSnapshot(saved)
					if err != nil {
						return err
					}
					checkpoint = stored.Workflow
					return validateGoImagesSnapshot(ModeNormal, saved)
				},
				Persist: func() error { return nil },
			})
			if err == nil || !strings.Contains(err.Error(), result) ||
				errors.Is(err, coordinator.ErrCanceled) != (result == "canceled") {

				t.Fatalf("terminal error = %v", err)
			}
			state := run.state.Workflow
			if !state.Complete || state.Result != result || !checkpoint.Complete || checkpoint.Result != result {
				t.Fatalf("state = %#v, checkpoint = %#v", state, checkpoint)
			}
		})
	}
}

func TestQueueRequiresPersistedIntent(t *testing.T) {
	service := &fakeService{}
	run := newWorkflowTestRun(t, testInput, nil, service)
	steps, err := run.Build()
	if err != nil {
		t.Fatal(err)
	}
	failure := errors.New("cannot save queue intent")
	attempts := 0
	queueAttempted := false
	var runner coordinator.StepRunner
	err = runner.ExecuteWithCallbacks(context.Background(), steps, coordinator.ExecutionCallbacks{
		Capture: func() error {
			queueAttempted = run.state.Workflow.QueueAttempted
			return nil
		},
		Persist: func() error {
			if queueAttempted {
				attempts++
				return failure
			}
			return nil
		},
	})
	if !errors.Is(err, failure) || service.queues != 0 || attempts != 3 {
		t.Fatalf("error=%v, queues=%d, intent save attempts=%d", err, service.queues, attempts)
	}
}

func TestGraphRecoveryHonorsSavedTerminalOutcome(t *testing.T) {
	for _, result := range []string{"succeeded", "failed", "canceled"} {
		t.Run(result, func(t *testing.T) {
			state, err := NewState(testInput)
			if err != nil {
				t.Fatal(err)
			}
			state.VerifiedMirroredCommit = testInput.SourceVersion
			state.QueueAttempted, state.BuildID = true, "888"
			state.Complete, state.Result = true, result
			service := &fakeService{pollErr: errors.New("provider unavailable")}
			run := newWorkflowTestRun(t, testInput, state, service)
			steps, err := run.Build()
			if err != nil {
				t.Fatal(err)
			}
			err = (&coordinator.StepRunner{}).Execute(context.Background(), steps)
			if (err == nil) != (result == "succeeded") ||
				errors.Is(err, coordinator.ErrCanceled) != (result == "canceled") ||
				service.polls != 0 || service.queues != 0 || service.mirrors != 0 {

				t.Fatalf("saved outcome %s was lost: error=%v, service=%#v", result, err, service)
			}
		})
	}
}

func TestGraphLeavesTransientPollFailureIncomplete(t *testing.T) {
	pollErr := errors.New("pipeline read unavailable")
	service := &fakeService{pollErr: pollErr}
	run := newWorkflowTestRun(t, testInput, nil, service)
	steps, err := run.Build()
	if err != nil {
		t.Fatal(err)
	}
	var runner coordinator.StepRunner
	if err := runner.Execute(context.Background(), steps); !errors.Is(err, pollErr) {
		t.Fatalf("error = %v, want %v", err, pollErr)
	}
	state := run.state.Workflow
	if state.Complete || state.Result != "" {
		t.Fatalf("state = %#v, want resumable incomplete state", state)
	}
}

func TestGraphBlocksQueueUntilMirrorAvailable(t *testing.T) {
	mirrorErr := errors.New("not mirrored")
	service := &fakeService{mirrorErr: mirrorErr}
	run := newWorkflowTestRun(t, testInput, nil, service)
	steps, err := run.Build()
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
	for _, status := range []coordinator.StepStatus{coordinator.StepStatusSucceeded, coordinator.StepStatusCanceled} {
		t.Run(string(status), func(t *testing.T) {
			state, err := NewState(testInput)
			if err != nil {
				t.Fatal(err)
			}
			state.QueueAttempted = true
			state.BuildID = "888"
			state.VerifiedMirroredCommit = workflowTestCommit
			service := &fakeService{status: status}
			run := newWorkflowTestRun(t, testInput, state, service)
			steps, err := run.Build()
			if err != nil {
				t.Fatal(err)
			}
			var runner coordinator.StepRunner
			err = runner.Execute(context.Background(), steps)
			state = &run.state.Workflow
			if status == coordinator.StepStatusSucceeded && err != nil ||
				status == coordinator.StepStatusCanceled && !errors.Is(err, coordinator.ErrCanceled) {

				t.Fatalf("resumed build result = %v, want %s", err, status)
			}
			if service.mirrors != 0 || service.queues != 0 || service.polls != 1 ||
				!state.Complete || state.Result != string(status) {

				t.Fatalf("service = %#v, state = %#v", service, state)
			}
		})
	}
}

func TestGraphRejectsKnownBuildWithoutMirrorCheckpoint(t *testing.T) {
	state, err := NewState(testInput)
	if err != nil {
		t.Fatal(err)
	}
	state.QueueAttempted = true
	state.BuildID = "888"
	run := newWorkflowTestRun(t, testInput, state, &fakeService{})
	if _, err := run.Build(); err == nil {
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

func TestBuildCapturesStateBeforeCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	service := &fakeService{}
	run := newWorkflowTestRun(t, testInput, nil, service)
	steps, err := run.Build()
	if err != nil {
		t.Fatal(err)
	}
	captures := 0
	var captured goImagesSnapshotState
	err = (&coordinator.StepRunner{}).ExecuteWithCallbacks(ctx, steps, coordinator.ExecutionCallbacks{
		Capture: func() error {
			captures++
			saved, err := run.TakeSnapshot()
			if err != nil {
				return err
			}
			_, captured, err = decodeGoImagesSnapshot(saved)
			cancel()
			return err
		},
		Persist: func() error { return nil },
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("update error = %v, want cancellation", err)
	}
	if captures != 1 || captured.Workflow.VerifiedMirroredCommit != testInput.SourceVersion ||
		service.queues != 0 || service.polls != 0 {

		t.Fatalf("captures=%d, state=%#v, service=%#v", captures, captured, service)
	}
}
