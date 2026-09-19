// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package goinfra

import (
	"context"
	"testing"

	"github.com/microsoft/go-infra/releaseui/coordinator"
)

func TestLoadResumesKnownWorkflowRunWithoutDispatchingAgain(t *testing.T) {
	queued := testGoInfraWorkflowRun("queued", "")
	github := &fakeGoInfraGitHub{
		workflowRun: queued,
		workflowUpdates: []GoInfraWorkflowRun{
			testGoInfraWorkflowRun("completed", "success"),
		},
	}
	process := NewProcess(github.integration()).Processes()[1]
	snapshot, err := process.Prepare(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	input, state, err := decodeGoInfraSnapshot(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	state.WorkflowRun = &queued
	snapshot, err = encodeGoInfraSnapshot(input, state)
	if err != nil {
		t.Fatal(err)
	}
	run, err := process.Load(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	steps, err := run.Build(context.Background(), func() {})
	if err != nil {
		t.Fatal(err)
	}
	if err := (&coordinator.StepRunner{}).Execute(context.Background(), steps); err != nil {
		t.Fatal(err)
	}
	stats := github.stats()
	if len(stats.dispatches) != 0 || stats.pollCalls != 1 {
		t.Fatalf("stats = %#v", stats)
	}
}
