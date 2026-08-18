// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package goinfragithub

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"
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

func TestPreflight(t *testing.T) {
	workflow := `on:
  workflow_dispatch:
    inputs:
      dry-run:
        required: true
        type: boolean
        default: false
`
	runner := &fakeRunner{responses: [][]byte{
		nil,
		[]byte(`{"full_name":"microsoft/go-infra","default_branch":"main"}`),
		[]byte(`{"path":".github/workflows/create-go-infra-patch-release.yml","state":"active"}`),
		[]byte(`{"encoding":"base64","content":"` + base64.StdEncoding.EncodeToString([]byte(workflow)) + `"}`),
	}}
	service, err := New(runner)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.Preflight(context.Background()); err != nil {
		t.Fatal(err)
	}
	want := [][]string{
		{"auth", "status", "--hostname", "github.com"},
		{"api", "--hostname", "github.com", "repos/microsoft/go-infra"},
		{"api", "--hostname", "github.com", "repos/microsoft/go-infra/actions/workflows/create-go-infra-patch-release.yml"},
		{"api", "--hostname", "github.com", "repos/microsoft/go-infra/contents/.github/workflows/create-go-infra-patch-release.yml?ref=main"},
	}
	for i, call := range runner.calls {
		if !reflect.DeepEqual(call.args, want[i]) {
			t.Fatalf("call %d args = %v, want %v", i, call.args, want[i])
		}
	}
}

func TestPreflightRejectsWorkflowInputDrift(t *testing.T) {
	workflow := `on:
  workflow_dispatch:
    inputs:
      version:
        required: true
        type: string
`
	runner := &fakeRunner{responses: [][]byte{
		nil,
		[]byte(`{"full_name":"microsoft/go-infra","default_branch":"main"}`),
		[]byte(`{"path":".github/workflows/create-go-infra-patch-release.yml","state":"active"}`),
		[]byte(`{"encoding":"base64","content":"` + base64.StdEncoding.EncodeToString([]byte(workflow)) + `"}`),
	}}
	service, _ := New(runner)
	if _, err := service.Preflight(context.Background()); err == nil {
		t.Fatal("workflow input drift was accepted")
	}
}

func TestRepositoryWorkflowContract(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("..", "..", "..", "..", ".github", "workflows", WorkflowFile))
	if err != nil {
		t.Fatal(err)
	}
	if err := validateWorkflowContract(data); err != nil {
		t.Fatal(err)
	}
}

func TestGetPullRequestRejectsFork(t *testing.T) {
	runner := &fakeRunner{responses: [][]byte{[]byte(`{
        "number":42,"title":"Release change","html_url":"https://github.com/microsoft/go-infra/pull/42",
        "state":"open","merged":false,"base":{"ref":"main"},
        "head":{"ref":"feature","sha":"0123456789abcdef0123456789abcdef01234567","repo":{"fork":true}}
    }`)}}
	service, _ := New(runner)
	if _, err := service.GetPullRequest(context.Background(), 42); err == nil {
		t.Fatal("fork pull request was accepted")
	}
}

func TestAddReleaseOnMergeLabel(t *testing.T) {
	runner := &fakeRunner{responses: [][]byte{
		[]byte(`{
            "number":42,"title":"Release change","html_url":"https://github.com/microsoft/go-infra/pull/42",
            "state":"open","merged":false,"base":{"ref":"main"},
            "head":{"ref":"feature","sha":"0123456789abcdef0123456789abcdef01234567","repo":{"fork":false}},
            "labels":[]
        }`),
		[]byte(`[{"name":"release-on-merge"}]`),
	}}
	service, _ := New(runner)
	pullRequest, err := service.AddReleaseOnMergeLabel(context.Background(), 42, "0123456789abcdef0123456789abcdef01234567")
	if err != nil {
		t.Fatal(err)
	}
	if pullRequest.Number != 42 || len(runner.calls) != 2 {
		t.Fatalf("pull request = %#v, calls = %d", pullRequest, len(runner.calls))
	}
	wantArgs := []string{"api", "--hostname", "github.com", "--method", "POST", "repos/microsoft/go-infra/issues/42/labels", "--input", "-"}
	if !reflect.DeepEqual(runner.calls[1].args, wantArgs) {
		t.Fatalf("args = %v, want %v", runner.calls[1].args, wantArgs)
	}
	var request struct {
		Labels []string `json:"labels"`
	}
	if err := json.Unmarshal(runner.calls[1].input, &request); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(request.Labels, []string{ReleaseLabel}) {
		t.Fatalf("labels = %v", request.Labels)
	}
}

func TestAddReleaseOnMergeLabelIsIdempotent(t *testing.T) {
	runner := &fakeRunner{responses: [][]byte{[]byte(`{
        "number":42,"title":"Release change","state":"open","merged":false,"base":{"ref":"main"},
        "head":{"ref":"feature","sha":"0123456789abcdef0123456789abcdef01234567","repo":{"fork":false}},
        "labels":[{"name":"release-on-merge"}]
    }`)}}
	service, _ := New(runner)
	if _, err := service.AddReleaseOnMergeLabel(context.Background(), 42, "0123456789abcdef0123456789abcdef01234567"); err != nil {
		t.Fatal(err)
	}
	if len(runner.calls) != 1 {
		t.Fatalf("calls = %d, want one read and no mutation", len(runner.calls))
	}
}

func TestAddReleaseOnMergeLabelRejectsChangedPR(t *testing.T) {
	runner := &fakeRunner{responses: [][]byte{[]byte(`{
        "number":42,"title":"Release change","html_url":"https://github.com/microsoft/go-infra/pull/42",
        "state":"open","merged":false,"base":{"ref":"main"},
        "head":{"ref":"feature","sha":"0123456789abcdef0123456789abcdef01234567","repo":{"fork":false}}
    }`)}}
	service, _ := New(runner)
	if _, err := service.AddReleaseOnMergeLabel(context.Background(), 42, "ffffffffffffffffffffffffffffffffffffffff"); err == nil {
		t.Fatal("changed pull request was labeled")
	}
	if len(runner.calls) != 1 {
		t.Fatalf("calls = %d", len(runner.calls))
	}
}

func TestDispatchPatchRelease(t *testing.T) {
	runner := &fakeRunner{responses: [][]byte{nil}}
	service, _ := New(runner)
	if err := service.DispatchPatchRelease(context.Background(), true); err != nil {
		t.Fatal(err)
	}
	wantArgs := []string{
		"api", "--hostname", "github.com", "--method", "POST",
		"repos/microsoft/go-infra/actions/workflows/create-go-infra-patch-release.yml/dispatches", "--input", "-",
	}
	if !reflect.DeepEqual(runner.calls[0].args, wantArgs) {
		t.Fatalf("args = %v, want %v", runner.calls[0].args, wantArgs)
	}
	var request struct {
		Ref    string            `json:"ref"`
		Inputs map[string]string `json:"inputs"`
	}
	if err := json.Unmarshal(runner.calls[0].input, &request); err != nil {
		t.Fatal(err)
	}
	if request.Ref != DefaultRef || request.Inputs["dry-run"] != "true" {
		t.Fatalf("request = %#v", request)
	}
}

func TestRunnerErrorIsReturned(t *testing.T) {
	runner := &fakeRunner{errors: []error{errors.New("not authenticated")}}
	service, _ := New(runner)
	if _, err := service.Preflight(context.Background()); err == nil {
		t.Fatal("preflight unexpectedly succeeded")
	}
}
