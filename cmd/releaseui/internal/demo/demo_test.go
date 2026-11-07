// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package demo

import (
	"context"
	"encoding/json"
	"reflect"
	"slices"
	"testing"

	"github.com/microsoft/go-infra/releaseui/contract"
	"github.com/microsoft/go-infra/releaseui/releaseinput"
	"github.com/microsoft/go-infra/releaseui/snapshot"
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
	for index, id := range []string{"demo-science-" + stateSchema, "demo-bad-science-" + stateSchema} {
		process := processes[index]
		definition := process.Definition()
		if definition.ID != id || definition.Name == "" {
			t.Fatalf("definition = %#v", definition)
		}
		if warning, blocking := process.Preflight(context.Background()); (warning != nil) != (index == 1) || blocking != nil {
			t.Fatalf("preflight = %v, %v", warning, blocking)
		}
		inputs := new(releaseinput.Set)
		result, ok := process.InputForm(inputs).(*input)
		if !ok || result == nil {
			t.Fatalf("input form = %#v", result)
		}
		if err := inputs.Parse(json.RawMessage(`{"input1":"3"}`)); err != nil {
			t.Fatal(err)
		}
		if result.Dimensions != 3 {
			t.Fatalf("dimensional order = %d", result.Dimensions)
		}
		if err := inputs.Parse(json.RawMessage(`{"input1":"3","unexpected":"1"}`)); err == nil {
			t.Fatal("demo accepted an undeclared input")
		}
		run := prepareTestRun(t, process)
		before := testSnapshot(t, run)
		savedInput, savedState, err := snapshot.Decode[input, state](before)
		if err != nil || savedInput.Dimensions != 2 || len(savedState.Determinants) != 2 {
			t.Fatalf("demo snapshot input=%#v, state=%#v, error=%v", savedInput, savedState, err)
		}
		plan := run.Plan()
		steps, err := run.Build()
		if err != nil {
			t.Fatal(err)
		}
		if plan == nil || plan.Subtitle != definition.Name ||
			plan.ExecutionButtonLabel != "Calculate the number" || len(plan.Facts) == 0 {

			t.Fatalf("plan = %#v", plan)
		}
		if !reflect.DeepEqual(before, testSnapshot(t, run)) {
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
			"Analyze dimension 1":  nil,
			"Analyze dimension 2":  nil,
			"Calculate the number": {"Analyze dimension 1", "Analyze dimension 2"},
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
	snapshot, err := process.Prepare(context.Background(), &input{Dimensions: 2})
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
	want := testSnapshot(t, second)
	snapshot.Input[0] = '!'
	snapshot.State[0] = '!'
	snapshotCopy := testSnapshot(t, first)
	snapshotCopy.Input[0] = '!'
	snapshotCopy.State[0] = '!'
	view := first.TakeView()
	view.Summary = "changed"
	plan := first.Plan()
	plan.Facts[0].Value = "changed"
	if !reflect.DeepEqual(testSnapshot(t, first), want) || !reflect.DeepEqual(testSnapshot(t, second), want) {
		t.Fatal("a snapshot mutation changed a loaded run")
	}
	if first.TakeView().Summary != "Ready" || first.Plan().Facts[0].Value == "changed" {
		t.Fatal("a returned view or plan aliases run state")
	}
	first.(*run).state.Determinants[0] = 2
	if second.(*run).state.Determinants[0] != 0 {
		t.Fatal("loaded runs share determinant results")
	}
}

func TestValidateRejectsInvalidState(t *testing.T) {
	process := NewProcess().Processes()[0]
	tests := []struct {
		name   string
		change func(*state)
	}{
		{"negative determinant", func(s *state) { s.Determinants[0] = -1 }},
		{"incorrect determinant", func(s *state) { s.Determinants[0] = 4 }},
		{"wrong dimension", func(s *state) { s.Determinants[1] = 2 }},
		{"premature estimate", func(s *state) { s.Number = 2.5 }},
		{"incorrect estimate", func(s *state) {
			s.Determinants = []float64{2, 2.25}
			s.Number = 2.25
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			state := state{Determinants: make([]float64, 2)}
			test.change(&state)
			snapshot, err := snapshot.Encode(input{Dimensions: 2}, state)
			if err != nil {
				t.Fatal(err)
			}
			if err := process.Validate(snapshot); err == nil {
				t.Fatal("invalid state was accepted for persistence")
			}
		})
	}
}

func TestValidateRejectsInvalidDimensions(t *testing.T) {
	process := NewProcess().Processes()[0]
	for _, test := range []struct {
		name       string
		dimensions int
		results    []float64
	}{
		{name: "zero dimensions"},
		{name: "negative dimensions", dimensions: -1},
		{name: "too many dimensions", dimensions: 51, results: make([]float64, 51)},
		{name: "missing determinants", dimensions: 2},
		{name: "too few determinants", dimensions: 2, results: []float64{0}},
		{name: "too many determinants", dimensions: 2, results: []float64{0, 0, 0}},
	} {
		t.Run(test.name, func(t *testing.T) {
			saved, err := snapshot.Encode(input{Dimensions: test.dimensions}, state{Determinants: test.results})
			if err != nil {
				t.Fatal(err)
			}
			if err := process.Validate(saved); err == nil {
				t.Fatal("invalid dimensional state was accepted")
			}
		})
	}
}

func TestLoadRejectsInvalidSnapshot(t *testing.T) {
	processes := NewProcess().Processes()
	valid := testSnapshot(t, prepareTestRun(t, processes[0]))
	if _, err := processes[0].Load(nil); err == nil {
		t.Fatal("nil snapshot was accepted")
	}
	tests := []struct {
		name  string
		input json.RawMessage
		state json.RawMessage
	}{
		{"unknown input", json.RawMessage(`{"extra":true}`), valid.State},
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

func TestLoadUsesConfiguredDemoName(t *testing.T) {
	processes := NewProcess().Processes()
	snapshot := testSnapshot(t, prepareTestRun(t, processes[0]))
	for _, process := range processes {
		run, err := process.Load(snapshot)
		if err != nil {
			t.Fatal(err)
		}
		if run.Plan().Subtitle != process.Definition().Name {
			t.Fatalf("plan = %#v, want configured demo identity", run.Plan())
		}
	}
}

func prepareTestRun(t *testing.T, process contract.Process) *run {
	t.Helper()
	snapshot, err := process.Prepare(context.Background(), &input{Dimensions: 2})
	if err != nil {
		t.Fatal(err)
	}
	if err := process.Validate(snapshot); err != nil {
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

func testSnapshot(t *testing.T, run contract.Run) *contract.StateSnapshot {
	t.Helper()
	snapshot, err := run.TakeSnapshot()
	if err != nil {
		t.Fatal(err)
	}
	return snapshot
}

func testState(t *testing.T, run contract.Run) state {
	t.Helper()
	_, state, err := snapshot.Decode[input, state](testSnapshot(t, run))
	if err != nil {
		t.Fatal(err)
	}
	return state
}
