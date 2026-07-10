// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package main

import (
	"slices"
	"strings"
	"testing"
)

func TestJobURLs(t *testing.T) {
	// A single page with: a benchmark job whose compare step ran, a benchmark
	// job whose compare step was skipped (no deep link), and a non-benchmark
	// job that must be ignored.
	input := `{
	  "total_count": 3,
	  "jobs": [
	    {
	      "name": "benchmark / bench (windows-2022-go1.25)",
	      "html_url": "https://github.com/o/r/actions/runs/1/job/11",
	      "steps": [
	        {"name": "Set up job", "number": 1, "conclusion": "success"},
	        {"name": "📊 Compare and check", "number": 7, "conclusion": "success"}
	      ]
	    },
	    {
	      "name": "benchmark / bench (windows-2025-go1.26)",
	      "html_url": "https://github.com/o/r/actions/runs/1/job/12",
	      "steps": [
	        {"name": "Compare and check", "number": 7, "conclusion": "skipped"}
	      ]
	    },
	    {
	      "name": "conclude",
	      "html_url": "https://github.com/o/r/actions/runs/1/job/13",
	      "steps": [
	        {"name": "Compare and check", "number": 7, "conclusion": "success"}
	      ]
	    }
	  ]
	}`

	got, err := jobURLs(strings.NewReader(input))
	if err != nil {
		t.Fatal(err)
	}
	want := []string{
		"windows-2022-go1.25\thttps://github.com/o/r/actions/runs/1/job/11#step:7:1",
		"windows-2025-go1.26\thttps://github.com/o/r/actions/runs/1/job/12",
	}
	if !slices.Equal(got, want) {
		t.Errorf("jobURLs = %#v, want %#v", got, want)
	}
}

func TestJobURLs_MultiplePages(t *testing.T) {
	// `gh api --paginate` concatenates one JSON object per page; jobURLs must
	// read them all.
	page1 := `{"jobs":[{"name":"b / bench (a)","html_url":"https://x/1","steps":[{"name":"Compare","number":3,"conclusion":"success"}]}]}`
	page2 := `{"jobs":[{"name":"b / bench (c)","html_url":"https://x/2","steps":[]}]}`

	got, err := jobURLs(strings.NewReader(page1 + "\n" + page2 + "\n"))
	if err != nil {
		t.Fatal(err)
	}
	want := []string{
		"a\thttps://x/1#step:3:1",
		"c\thttps://x/2",
	}
	if !slices.Equal(got, want) {
		t.Errorf("jobURLs = %#v, want %#v", got, want)
	}
}

func TestJobURLs_Empty(t *testing.T) {
	got, err := jobURLs(strings.NewReader(`{"jobs":[]}`))
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 0 {
		t.Errorf("expected no lines, got %#v", got)
	}
}
