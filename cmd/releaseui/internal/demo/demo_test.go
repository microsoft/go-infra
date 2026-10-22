// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package demo

import (
	"context"
	"encoding/json"
	"reflect"
	"slices"
	"testing"
	"time"

	"github.com/microsoft/go-infra/releaseui/contract"
	"github.com/microsoft/go-infra/releaseui/releaseinput"
)

func TestProcessGroup(t *testing.T) {
	group := NewProcess()
	if identity := group.Identity(); identity.Name != "Demo" || identity.Mark == "" {
		t.Fatalf("identity = %#v", identity)
	}
	processes := group.Processes()
	if len(processes) != 2 {
		t.Fatalf("process count = %d, want 2", len(processes))
	}
	for index, id := range []string{"demo-a", "demo-b"} {
		process := processes[index]
		definition := process.Definition()
		if definition.ID != id || definition.Notice == nil || definition.Notice.Title == "" {
			t.Fatalf("definition = %#v", definition)
		}
		if warning, blocking := process.Preflight(context.Background()); warning != nil || blocking != nil {
			t.Fatalf("preflight = %v, %v", warning, blocking)
		}
		inputs := new(releaseinput.Set)
		if result := process.InputForm(inputs); result != nil {
			t.Fatalf("input form = %#v", result)
		}
		if err := inputs.Parse(json.RawMessage(`{}`)); err != nil {
			t.Fatal(err)
		}
		if err := inputs.Parse(json.RawMessage(`{"unexpected":"1"}`)); err == nil {
			t.Fatal("demo accepted an undeclared input")
		}
		run := prepareTestRun(t, process)
		before := run.TakeSnapshot()
		plan := run.Plan()
		steps, err := run.Build(context.Background(), nil)
		if err != nil {
			t.Fatal(err)
		}
		if plan == nil || plan.Subtitle != definition.Name+" simulated release" ||
			plan.ExecutionButtonLabel != "Run demo" || len(plan.Facts) == 0 {

			t.Fatalf("plan = %#v", plan)
		}
		if !reflect.DeepEqual(before, run.TakeSnapshot()) {
			t.Fatal("Plan or Build changed the prepared snapshot")
		}
		dependencies := make(map[string][]string)
		for _, step := range steps {
			if step.Timeout <= 0 || step.Func == nil {
				t.Fatalf("invalid step = %#v", step)
			}
			var names []string
			for _, dependency := range step.DependsOn {
				names = append(names, dependency.Name)
			}
			slices.Sort(names)
			dependencies[step.Name] = names
		}
		want := map[string][]string{
			"Initialize demo": nil,
			"Fake work A":     {"Initialize demo"},
			"Fake work B":     {"Initialize demo"},
			"Finish demo":     {"Fake work A", "Fake work B"},
		}
		if len(steps) != len(want) || !reflect.DeepEqual(dependencies, want) {
			t.Fatalf("dependencies = %#v, want %#v", dependencies, want)
		}
		if view := run.TakeView(); !view.Test || view.RunURL != "" || view.Summary != "Ready" {
			t.Fatalf("view = %#v", view)
		}
	}
}

func TestSnapshotIsolation(t *testing.T) {
	process := NewProcess().Processes()[0]
	snapshot, err := process.Prepare(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	first, err := process.Load(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	second, err := process.Load(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	want := second.TakeSnapshot()
	snapshot.Input[0] = '!'
	snapshot.State[0] = '!'
	snapshotCopy := first.TakeSnapshot()
	snapshotCopy.Input[0] = '!'
	snapshotCopy.State[0] = '!'
	view := first.TakeView()
	view.Summary = "changed"
	plan := first.Plan()
	plan.Facts[0].Value = "changed"
	if !reflect.DeepEqual(first.TakeSnapshot(), want) || !reflect.DeepEqual(second.TakeSnapshot(), want) {
		t.Fatal("a snapshot mutation changed a loaded run")
	}
	if first.TakeView().Summary != "Ready" || first.Plan().Facts[0].Value == "changed" {
		t.Fatal("a returned view or plan aliases run state")
	}
}

func TestLoadRejectsInvalidState(t *testing.T) {
	process := NewProcess().Processes()[0]
	tests := []struct {
		name   string
		change func(*state)
	}{
		{"schema", func(s *state) { s.SchemaVersion++ }},
		{"missing time", func(s *state) { s.UpdatedAt = time.Time{} }},
		{"negative work A", func(s *state) { s.WorkACompleted = -1 }},
		{"excess work A", func(s *state) { s.Initialized = true; s.WorkACompleted = workUnits + 1 }},
		{"negative work B", func(s *state) { s.WorkBCompleted = -1 }},
		{"excess work B", func(s *state) { s.Initialized = true; s.WorkBCompleted = workUnits + 1 }},
		{"work before initialization", func(s *state) { s.WorkACompleted = 1 }},
		{"finish before work", func(s *state) { s.Initialized = true; s.Finished = true }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			state := state{SchemaVersion: stateSchema, UpdatedAt: time.Now().UTC()}
			test.change(&state)
			snapshot, err := encodeSnapshot(input{ProcessID: process.Definition().ID}, state)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := process.Load(snapshot); err == nil {
				t.Fatal("invalid state was accepted")
			}
		})
	}
}

func TestLoadRejectsInvalidSnapshot(t *testing.T) {
	processes := NewProcess().Processes()
	valid := prepareTestRun(t, processes[0]).TakeSnapshot()
	if _, err := processes[0].Load(nil); err == nil {
		t.Fatal("nil snapshot was accepted")
	}
	if _, err := processes[1].Load(valid); err == nil {
		t.Fatal("snapshot from another process was accepted")
	}
	tests := []struct {
		name  string
		input json.RawMessage
		state json.RawMessage
	}{
		{"unknown input", json.RawMessage(`{"processId":"demo-a","extra":true}`), valid.State},
		{"null input", json.RawMessage(`null`), valid.State},
		{"invalid input", json.RawMessage(`{`), valid.State},
		{"extra input value", append(slices.Clone(valid.Input), []byte(` {}`)...), valid.State},
		{"unknown state", valid.Input, json.RawMessage(`{"extra":true}`)},
		{"null state", valid.Input, json.RawMessage(`null`)},
		{"invalid state", valid.Input, json.RawMessage(`{`)},
		{"extra state value", valid.Input, append(slices.Clone(valid.State), []byte(` {}`)...)},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if _, err := processes[0].Load(&contract.StateSnapshot{
				Input: test.input, State: test.state,
			}); err == nil {
				t.Fatal("invalid snapshot was accepted")
			}
		})
	}
}

func prepareTestRun(t *testing.T, process contract.Process) *run {
	t.Helper()
	snapshot, err := process.Prepare(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	loaded, err := process.Load(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	run, ok := loaded.(*run)
	if !ok {
		t.Fatalf("loaded run type = %T", loaded)
	}
	return run
}

func testState(t *testing.T, run contract.Run) state {
	t.Helper()
	state, err := decodeStrictJSON[state](run.TakeSnapshot().State)
	if err != nil {
		t.Fatal(err)
	}
	return state
}
