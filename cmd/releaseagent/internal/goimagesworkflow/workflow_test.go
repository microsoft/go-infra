// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package goimagesworkflow

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/microsoft/go-infra/cmd/releaseagent/internal/coordinator"
)

const testCommit = "81ce9afc2b75ec4e153dd15fc3c7539b12024945"

var testInput = &Input{
	Versions: []string{"1.25.12-1", "1.26.5-2"}, Mode: ModeNormal,
	SourceBranch: "refs/heads/microsoft/main", SourceVersion: testCommit,
	MirrorTarget: InternalMirrorTarget, PipelineID: 1023,
}

type fakeService struct {
	mirrorErr error
	mirrors   int
	queues    int
	polls     int
}

func (service *fakeService) PollMirror(_ context.Context, target, commit string) error {
	service.mirrors++
	if target != InternalMirrorTarget || commit != testCommit {
		return errors.New("unexpected mirror target")
	}
	return service.mirrorErr
}

func (service *fakeService) QueuePipeline(_ context.Context, pipelineID int, parameters map[string]string) (string, error) {
	service.queues++
	if pipelineID != 1023 || parameters["publishRepoPrefix"] != "public/" {
		return "", errors.New("unexpected queue request")
	}
	return "888", nil
}

func (service *fakeService) PollPipeline(_ context.Context, buildID string) error {
	service.polls++
	if buildID != "888" {
		return errors.New("unexpected build ID")
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
	var checkpoints []State
	steps, state, err := NewGraphWithCheckpoint(testInput, nil, service, func(_ context.Context, state *State) error {
		data, err := json.Marshal(state)
		if err != nil {
			return err
		}
		var clone State
		if err := json.Unmarshal(data, &clone); err != nil {
			return err
		}
		checkpoints = append(checkpoints, clone)
		return nil
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
	if len(checkpoints) != 3 || !checkpoints[0].QueueAttempted || checkpoints[0].BuildID != "" ||
		checkpoints[1].BuildID != "888" || checkpoints[1].Complete {

		t.Fatalf("checkpoints = %#v", checkpoints)
	}
}

func TestGraphBlocksQueueUntilMirrorAvailable(t *testing.T) {
	mirrorErr := errors.New("not mirrored")
	service := &fakeService{mirrorErr: mirrorErr}
	steps, _, err := NewGraph(testInput, nil, service)
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
	state.BuildID = "888"
	service := &fakeService{}
	steps, state, err := NewGraph(testInput, state, service)
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

func TestStateAccessRetriesDirtyCheckpoint(t *testing.T) {
	checkpointErr := errors.New("checkpoint unavailable")
	fail := true
	checkpointCalls := 0
	state := &State{}
	access := &stateAccess{
		state: state,
		checkpoint: func(context.Context, *State) error {
			checkpointCalls++
			if fail {
				return checkpointErr
			}
			return nil
		},
	}
	if err := access.update(context.Background(), func(state *State) {
		state.QueueAttempted = true
	}); !errors.Is(err, checkpointErr) {
		t.Fatalf("update error = %v, want checkpoint error", err)
	}
	fail = false
	if err := access.flush(context.Background()); err != nil {
		t.Fatalf("flush failed: %v", err)
	}
	if checkpointCalls != 2 || !state.QueueAttempted {
		t.Fatalf("checkpoint calls = %d, state = %#v", checkpointCalls, state)
	}
}
