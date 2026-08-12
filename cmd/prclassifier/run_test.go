// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package main

import (
	"bytes"
	"context"
	"errors"
	"os"
	"slices"
	"strings"
	"testing"
)

type fakeGitHub struct {
	labels       []string
	pullNumbers  []int
	pulls        map[int]pullRequest
	files        map[int][]changedFile
	pullErrors   map[int]error
	created      []string
	removed      map[int][]string
	added        map[int][]string
	createErrors map[string]error
	getLabels    map[string]bool
}

func (f *fakeGitHub) ListLabels(context.Context, string, string) ([]string, error) {
	return slices.Clone(f.labels), nil
}

func (f *fakeGitHub) CreateLabel(_ context.Context, _, _ string, definition labelDefinition) error {
	f.created = append(f.created, definition.Name)
	return f.createErrors[definition.Name]
}

func (f *fakeGitHub) GetLabel(_ context.Context, _, _, label string) error {
	if f.getLabels[label] {
		return nil
	}
	return errors.New("label not found")
}

func (f *fakeGitHub) ListOpenPullRequests(context.Context, string, string) ([]int, error) {
	return slices.Clone(f.pullNumbers), nil
}

func (f *fakeGitHub) GetPullRequest(_ context.Context, _, _ string, number int) (pullRequest, error) {
	if err := f.pullErrors[number]; err != nil {
		return pullRequest{}, err
	}
	return f.pulls[number], nil
}

func (f *fakeGitHub) ListPullRequestFiles(_ context.Context, _, _ string, number int) ([]changedFile, error) {
	return slices.Clone(f.files[number]), nil
}

func (f *fakeGitHub) RemoveLabel(_ context.Context, _, _ string, number int, label string) error {
	if f.removed == nil {
		f.removed = make(map[int][]string)
	}
	f.removed[number] = append(f.removed[number], label)
	return nil
}

func (f *fakeGitHub) AddLabels(_ context.Context, _, _ string, number int, labels []string) error {
	if f.added == nil {
		f.added = make(map[int][]string)
	}
	f.added[number] = append(f.added[number], labels...)
	return nil
}

func TestExecuteReconcilesLabels(t *testing.T) {
	cfg := config{
		Owner:    "microsoft",
		Repo:     "agent-framework-go",
		PRNumber: 7,
		AreaPrefixes: map[string][]string{
			"area:agent":           {"agent"},
			"area:provider":        {"provider"},
			"area:provider/openai": {"provider/openaiprovider"},
		},
	}
	client := &fakeGitHub{
		labels: []string{"size:small", "kind:code"},
		pulls: map[int]pullRequest{7: {
			Labels:       []string{"manual", "size:large", "kind:docs", classificationFailedLabel, "area:agent"},
			Additions:    20,
			ChangedFiles: 1,
		}},
		files: map[int][]changedFile{7: {{Path: "provider/openaiprovider/responses.go"}}},
	}
	var output bytes.Buffer
	if err := execute(context.Background(), cfg, client, &output); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(client.removed[7], []string{"area:agent", "size:large"}) {
		t.Fatalf("removed = %#v", client.removed[7])
	}
	wantAdded := []string{"size:small", "area:provider", "area:provider/openai"}
	if !slices.Equal(client.added[7], wantAdded) {
		t.Fatalf("added = %#v, want %#v", client.added[7], wantAdded)
	}
	if strings.Contains(strings.Join(client.removed[7], ","), "manual") {
		t.Fatal("manual label was removed")
	}
	if strings.Contains(strings.Join(client.removed[7], ","), classificationFailedLabel) {
		t.Fatal("agent-owned failure label was removed")
	}
	if !strings.Contains(output.String(), "PR #7: size:small, area:provider") {
		t.Fatalf("output = %q", output.String())
	}
}

func TestExecuteBulkContinuesAndWritesSummary(t *testing.T) {
	summaryFile := t.TempDir() + "/summary.md"
	cfg := config{
		Owner:        "o",
		Repo:         "r",
		AreaPrefixes: map[string][]string{},
		SummaryPath:  summaryFile,
	}
	client := &fakeGitHub{
		pullNumbers: []int{2, 1},
		pulls: map[int]pullRequest{2: {
			Additions:    2,
			ChangedFiles: 1,
		}},
		files:      map[int][]changedFile{2: {{Path: "README.md"}}},
		pullErrors: map[int]error{1: errors.New("boom")},
	}
	var output bytes.Buffer
	err := execute(context.Background(), cfg, client, &output)
	if err == nil || !strings.Contains(err.Error(), "failed to classify 1") {
		t.Fatalf("error = %v", err)
	}
	data, readErr := os.ReadFile(summaryFile)
	if readErr != nil {
		t.Fatal(readErr)
	}
	summary := string(data)
	if !strings.Contains(summary, "Classified 1 pull request(s)") ||
		!strings.Contains(summary, "Failed PRs: #1") ||
		!strings.Contains(summary, "`size:small`") {

		t.Fatalf("summary = %q", summary)
	}
}

func TestTruncatedResponsePreservesExistingArea(t *testing.T) {
	cfg := config{
		Owner:        "o",
		Repo:         "r",
		PRNumber:     1,
		AreaPrefixes: map[string][]string{"area:agent": {"agent"}},
	}
	client := &fakeGitHub{
		pulls: map[int]pullRequest{1: {
			Labels:       []string{"area:agent", "size:small", "kind:docs"},
			ChangedFiles: maxPullRequestFiles + 1,
		}},
		files: map[int][]changedFile{1: {{Path: "README.md"}}},
	}
	if err := execute(context.Background(), cfg, client, &bytes.Buffer{}); err != nil {
		t.Fatal(err)
	}
	if slices.Contains(client.removed[1], "area:agent") {
		t.Fatalf("truncated classification removed area label: %#v", client.removed[1])
	}
}

func TestLabelLimitCheckedBeforeReconciliation(t *testing.T) {
	labels := make([]string, 99)
	for i := range labels {
		labels[i] = "manual-" + string(rune('a'+i%26)) + string(rune('A'+i/26))
	}
	cfg := config{
		Owner:        "o",
		Repo:         "r",
		PRNumber:     1,
		AreaPrefixes: map[string][]string{"area:agent": {"agent"}},
	}
	client := &fakeGitHub{
		pulls: map[int]pullRequest{1: {
			Labels:       labels,
			ChangedFiles: 1,
		}},
		files: map[int][]changedFile{1: {{Path: "agent/agent.go"}}},
	}
	err := execute(context.Background(), cfg, client, &bytes.Buffer{})
	if err == nil || !strings.Contains(err.Error(), "failed to classify 1") {
		t.Fatalf("error = %v", err)
	}
	if len(client.removed[1]) != 0 || len(client.added[1]) != 0 {
		t.Fatalf("labels mutated before limit check: removed=%v added=%v", client.removed[1], client.added[1])
	}
}

func TestEnsureLabelsAcceptsConcurrentCreation(t *testing.T) {
	cfg := config{Owner: "o", Repo: "r", AreaPrefixes: map[string][]string{}}
	client := &fakeGitHub{
		createErrors: map[string]error{"size:small": errors.New("already exists")},
		getLabels:    map[string]bool{"size:small": true},
	}
	if err := ensureLabels(context.Background(), cfg, client, labelDefinitions(cfg)); err != nil {
		t.Fatal(err)
	}
}
