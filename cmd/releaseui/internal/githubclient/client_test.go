// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package githubclient

import (
	"context"
	"encoding/json"
	"reflect"
	"testing"
	"time"
)

type commandCall struct {
	input []byte
	args  []string
}

type fakeRunner struct {
	responses [][]byte
	errors    []error
	calls     []commandCall
}

func (r *fakeRunner) Run(_ context.Context, input []byte, args ...string) ([]byte, error) {
	r.calls = append(r.calls, commandCall{input: append([]byte(nil), input...), args: append([]string(nil), args...)})
	index := len(r.calls) - 1
	var output []byte
	if index < len(r.responses) {
		output = r.responses[index]
	}
	if index < len(r.errors) {
		return output, r.errors[index]
	}
	return output, nil
}

func testWorkflow() Workflow {
	return Workflow{
		Repository:       Repository{Owner: "microsoft", Name: "example"},
		File:             "release.yml",
		Ref:              "main",
		CorrelationInput: "release-ui-correlation-id",
	}
}

func testRun(status, conclusion string) []byte {
	return testRunWithTitle(status, conclusion, "release-ui-test-correlation")
}

func testRunWithTitle(status, conclusion, title string) []byte {
	createdAt := time.Now().UTC().Format(time.RFC3339)
	return []byte(`{"id":123,"html_url":"https://github.com/microsoft/example/actions/runs/123","status":"` + status + `","conclusion":"` + conclusion + `","event":"workflow_dispatch","head_branch":"main","head_sha":"0123456789abcdef0123456789abcdef01234567","path":".github/workflows/release.yml","created_at":"` + createdAt + `","display_title":"` + title + `","actor":{"login":"release-runner"}}`)
}

func TestDispatchWorkflowDiscoversActorRun(t *testing.T) {
	runner := &fakeRunner{responses: [][]byte{
		[]byte(`{"login":"release-runner"}`),
		[]byte(`{"workflow_runs":[]}`),
		nil,
		[]byte(`{"workflow_runs":[` + string(testRun("queued", "")) + `]}`),
	}}
	client, err := New("github.com", runner)
	if err != nil {
		t.Fatal(err)
	}
	client.newCorrelationID = func() (string, error) { return "release-ui-test-correlation", nil }
	client.sleep = func(context.Context, time.Duration) error { return nil }
	client.discoveryPeriod = 100 * time.Millisecond
	run, err := client.DispatchWorkflow(context.Background(), testWorkflow(), map[string]string{"dry-run": "true"})
	if err != nil {
		t.Fatal(err)
	}
	if run.ID != 123 || run.Status != "queued" {
		t.Fatalf("run = %#v", run)
	}
	wantArgs := []string{
		"api", "--hostname", "github.com", "--method", "POST",
		"repos/microsoft/example/actions/workflows/release.yml/dispatches", "--input", "-",
	}
	if !reflect.DeepEqual(runner.calls[2].args, wantArgs) {
		t.Fatalf("args = %v, want %v", runner.calls[2].args, wantArgs)
	}
	var request struct {
		Ref    string            `json:"ref"`
		Inputs map[string]string `json:"inputs"`
	}
	if err := json.Unmarshal(runner.calls[2].input, &request); err != nil {
		t.Fatal(err)
	}
	if request.Ref != "main" || request.Inputs["dry-run"] != "true" ||
		request.Inputs["release-ui-correlation-id"] != "release-ui-test-correlation" {

		t.Fatalf("request = %#v", request)
	}
}

func TestDispatchWorkflowIgnoresConcurrentSameActorRun(t *testing.T) {
	runner := &fakeRunner{responses: [][]byte{
		[]byte(`{"login":"release-runner"}`),
		[]byte(`{"workflow_runs":[]}`),
		nil,
		[]byte(`{"workflow_runs":[` + string(testRunWithTitle("queued", "", "another-dispatch")) + `]}`),
		[]byte(`{"workflow_runs":[` + string(testRun("queued", "")) + `]}`),
	}}
	client, err := New("github.com", runner)
	if err != nil {
		t.Fatal(err)
	}
	client.newCorrelationID = func() (string, error) { return "release-ui-test-correlation", nil }
	client.sleep = func(context.Context, time.Duration) error { return nil }
	client.discoveryPeriod = 100 * time.Millisecond
	run, err := client.DispatchWorkflow(context.Background(), testWorkflow(), map[string]string{"dry-run": "true"})
	if err != nil {
		t.Fatal(err)
	}
	if run.DisplayTitle != "release-ui-test-correlation" || len(runner.calls) != 5 {
		t.Fatalf("run = %#v, calls = %d", run, len(runner.calls))
	}
}

func TestPollWorkflowRunWaitsForSuccess(t *testing.T) {
	runner := &fakeRunner{responses: [][]byte{testRun("queued", ""), testRun("in_progress", ""), testRun("completed", "success")}}
	client, err := New("github.com", runner)
	if err != nil {
		t.Fatal(err)
	}
	client.sleep = func(context.Context, time.Duration) error { return nil }
	var statuses []string
	completed, err := client.PollWorkflowRun(context.Background(), testWorkflow(), 123, func(run WorkflowRun) error {
		statuses = append(statuses, run.Status)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if completed.Conclusion != "success" || !reflect.DeepEqual(statuses, []string{"queued", "in_progress", "completed"}) {
		t.Fatalf("completed = %#v, statuses = %v", completed, statuses)
	}
}

func TestPollWorkflowRunReturnsFailure(t *testing.T) {
	runner := &fakeRunner{responses: [][]byte{testRun("completed", "failure")}}
	client, err := New("github.com", runner)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := client.PollWorkflowRun(context.Background(), testWorkflow(), 123, nil); err == nil {
		t.Fatal("failed workflow run was accepted")
	}
}
