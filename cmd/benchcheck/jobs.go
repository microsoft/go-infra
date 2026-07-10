// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
)

// jobsResponse is one page of the GitHub "list jobs for a workflow run" API
// response (https://docs.github.com/rest/actions/workflow-jobs). Only the
// fields benchcheck needs are decoded.
type jobsResponse struct {
	Jobs []jobInfo `json:"jobs"`
}

type jobInfo struct {
	Name    string    `json:"name"`
	HTMLURL string    `json:"html_url"`
	Steps   []jobStep `json:"steps"`
}

type jobStep struct {
	Name       string `json:"name"`
	Number     int    `json:"number"`
	Conclusion string `json:"conclusion"`
}

// benchJobMarker appears in a reusable-workflow matrix job's name, which looks
// like "<caller-job-name> / bench (<label>)". The text after the marker (minus
// the trailing ")") is the artifact label the report keys on.
const benchJobMarker = " / bench ("

func cmdJobURLs(args []string) {
	fs := flag.NewFlagSet("job-urls", flag.ExitOnError)
	repo := fs.String("repo", "", "GitHub repository (OWNER/REPO) whose run jobs to fetch via gh")
	runID := fs.String("id", "", "workflow run ID whose jobs to fetch via gh")
	fs.Usage = func() {
		fmt.Fprintf(os.Stderr, `usage: benchcheck job-urls -repo OWNER/REPO -id RUN_ID
       benchcheck job-urls [jobs.json]

Write a TSV mapping each benchmark artifact label to its job URL, deep-linked
to the compare step when available. The output is consumed by
"benchcheck report -job-urls".

With -repo and -id, the GitHub "list jobs for a workflow run" API is fetched
via gh (which handles authentication and pagination). Otherwise the API
response is read from the given file, or stdin if omitted.
`)
	}
	if err := fs.Parse(args); err != nil {
		os.Exit(2)
	}

	if (*repo == "") != (*runID == "") {
		fmt.Fprintln(os.Stderr, "benchcheck job-urls: -repo and -id must be used together")
		os.Exit(2)
	}
	if (*repo != "") && fs.NArg() > 0 {
		fmt.Fprintln(os.Stderr, "benchcheck job-urls: cannot use -repo/-id together with a file argument")
		os.Exit(2)
	}
	if *repo == "" && fs.NArg() > 1 {
		fs.Usage()
		os.Exit(2)
	}

	var in io.Reader
	switch {
	case *repo != "":
		data, err := fetchJobs(*repo, *runID)
		if err != nil {
			fmt.Fprintf(os.Stderr, "benchcheck job-urls: %v\n", err)
			os.Exit(1)
		}
		in = bytes.NewReader(data)
	case fs.NArg() == 1:
		f, err := os.Open(fs.Arg(0))
		if err != nil {
			fmt.Fprintf(os.Stderr, "benchcheck job-urls: %v\n", err)
			os.Exit(1)
		}
		defer f.Close()
		in = f
	default:
		in = os.Stdin
	}

	lines, err := jobURLs(in)
	if err != nil {
		fmt.Fprintf(os.Stderr, "benchcheck job-urls: %v\n", err)
		os.Exit(1)
	}

	out := bufio.NewWriter(os.Stdout)
	for _, line := range lines {
		fmt.Fprintln(out, line)
	}
	if err := out.Flush(); err != nil {
		fmt.Fprintf(os.Stderr, "benchcheck job-urls: %v\n", err)
		os.Exit(1)
	}
}

// fetchJobs runs `gh api` to retrieve the "list jobs for a workflow run" API
// response for repo and runID, following pagination. gh supplies auth from the
// GH_TOKEN/GITHUB_TOKEN environment. repo and runID come from trusted workflow
// context and are passed as single arguments (no shell), so they cannot inject.
func fetchJobs(repo, runID string) ([]byte, error) {
	endpoint := fmt.Sprintf("repos/%s/actions/runs/%s/jobs", repo, runID)
	cmd := exec.Command("gh", "api", endpoint, "--paginate")
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		if msg := strings.TrimSpace(stderr.String()); msg != "" {
			return nil, fmt.Errorf("gh api %s: %w: %s", endpoint, err, msg)
		}
		return nil, fmt.Errorf("gh api %s: %w", endpoint, err)
	}
	return out, nil
}

// jobURLs parses one or more concatenated pages of the jobs API response (as
// produced by `gh api --paginate`) and returns "<label>\t<url>" lines for the
// benchmark matrix jobs, deep-linking each URL to its compare step when found.
func jobURLs(r io.Reader) ([]string, error) {
	dec := json.NewDecoder(r)
	var lines []string
	for {
		var page jobsResponse
		if err := dec.Decode(&page); err != nil {
			if errors.Is(err, io.EOF) {
				break
			}
			return nil, err
		}
		for _, j := range page.Jobs {
			i := strings.Index(j.Name, benchJobMarker)
			if i < 0 {
				continue // not a benchmark matrix job
			}
			label := strings.TrimSuffix(j.Name[i+len(benchJobMarker):], ")")
			url := j.HTMLURL
			if n, ok := compareStepNumber(j.Steps); ok {
				url = fmt.Sprintf("%s#step:%d:1", url, n)
			}
			lines = append(lines, label+"\t"+url)
		}
	}
	return lines, nil
}

// compareStepNumber returns the number of the first non-skipped step whose name
// mentions "compare" (case-insensitive), so the report can deep-link to the
// step that produced the comparison.
func compareStepNumber(steps []jobStep) (int, bool) {
	for _, s := range steps {
		if s.Conclusion == "skipped" {
			continue
		}
		if strings.Contains(strings.ToLower(s.Name), "compare") {
			return s.Number, true
		}
	}
	return 0, false
}
