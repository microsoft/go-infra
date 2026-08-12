// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package main

import (
	"slices"
	"testing"
)

func TestClassifySize(t *testing.T) {
	tests := []struct {
		name  string
		lines int
		files int
		want  string
	}{
		{name: "small-boundary", lines: 30, files: 2, want: "size:small"},
		{name: "medium-by-lines", lines: 31, files: 2, want: "size:medium"},
		{name: "medium-boundary", lines: 100, files: 5, want: "size:medium"},
		{name: "large-by-files", lines: 50, files: 6, want: "size:large"},
		{name: "large-boundary", lines: 300, files: 10, want: "size:large"},
		{name: "xlarge-by-lines", lines: 301, files: 2, want: "size:xlarge"},
		{name: "xlarge-by-files", lines: 10, files: 11, want: "size:xlarge"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			files := make([]changedFile, tt.files)
			for i := range files {
				files[i].Path = "src/file.go"
			}
			got := classify(config{}, pullRequest{
				Additions:    tt.lines,
				ChangedFiles: tt.files,
			}, files)
			if got.Labels[0] != tt.want {
				t.Fatalf("size = %q, want %q", got.Labels[0], tt.want)
			}
		})
	}
}

func TestClassifyAreas(t *testing.T) {
	cfg := config{AreaPrefixes: map[string][]string{
		"area:provider":        {"provider"},
		"area:provider/openai": {"provider/openaiprovider"},
		"area:tooling":         {"cmd"},
	}}
	files := []changedFile{
		{Path: "provider/openaiprovider/responses.go"},
		{Path: "cmd/check/main.go"},
	}
	got := classify(cfg, pullRequest{Additions: 25, ChangedFiles: len(files)}, files)
	want := []string{
		"size:small",
		"area:provider",
		"area:provider/openai",
		"area:tooling",
	}
	if !slices.Equal(got.Labels, want) {
		t.Fatalf("labels = %#v, want %#v", got.Labels, want)
	}
}

func TestClassifyTruncatedFilesIsConservative(t *testing.T) {
	cfg := config{
		AreaPrefixes: map[string][]string{"area:agent": {"agent"}},
	}
	files := []changedFile{{Path: "examples/basic/main.go"}}
	got := classify(cfg, pullRequest{
		Additions:    1,
		ChangedFiles: maxPullRequestFiles + 1,
	}, files)
	if !got.FilesTruncated {
		t.Fatal("expected files response to be marked truncated")
	}
	if !slices.Equal(got.Labels, []string{"size:xlarge"}) {
		t.Fatalf("labels = %#v, want size:xlarge", got.Labels)
	}
}

func TestMatchesAnyPrefix(t *testing.T) {
	prefixes := []string{"agent", "cmd/tool.go"}
	for _, file := range []string{"agent", "agent/agent.go", "cmd/tool.go"} {
		if !matchesAnyPrefix(file, prefixes) {
			t.Errorf("expected %q to match", file)
		}
	}
	for _, file := range []string{"agentic/file.go", "cmd/tool.go.bak"} {
		if matchesAnyPrefix(file, prefixes) {
			t.Errorf("did not expect %q to match", file)
		}
	}
}
