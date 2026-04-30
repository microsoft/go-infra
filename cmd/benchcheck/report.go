// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"html"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

func cmdReport(args []string) {
	fs := flag.NewFlagSet("report", flag.ExitOnError)
	runURL := fs.String("run-url", "", "URL of the workflow run (fallback for job links)")
	jobURLsFile := fs.String("job-urls", "", "TSV file mapping artifact labels to job URLs")
	overallResult := fs.String("overall-result", "success", "overall bench job result (success/failure)")
	maxLen := fs.Int("max-len", 65536, "maximum output length for GitHub PR comment compatibility")
	fs.Usage = func() {
		fmt.Fprintf(os.Stderr, "usage: benchcheck report [flags] results-dir\n\nFlags:\n")
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		os.Exit(2)
	}

	if fs.NArg() != 1 {
		fs.Usage()
		os.Exit(2)
	}

	resultsDir := fs.Arg(0)
	jobURLs := loadJobURLs(*jobURLsFile)

	var buf strings.Builder

	// Marker for finding/updating existing PR comments.
	buf.WriteString("<!-- benchmark-results -->\n")

	// Header.
	buf.WriteString("## Benchmark Results\n\n")
	if *overallResult != "success" {
		buf.WriteString(":warning: **Issues detected** — expand failed jobs below for details\n\n")
	} else {
		buf.WriteString(":white_check_mark: **No significant regressions detected**\n\n")
	}

	// Process each artifact directory.
	pattern := filepath.Join(resultsDir, "benchstat-*")
	entries, err := filepath.Glob(pattern)
	if err != nil {
		fmt.Fprintf(os.Stderr, "benchcheck report: discovering benchmark results with %q: %v\n", pattern, err)
		buf.WriteString("<details open>\n")
		buf.WriteString("<summary>:boom: <code>artifact-discovery</code></summary>\n\n")
		buf.WriteString("**Benchmark result discovery failed:**\n```\n")
		buf.WriteString(err.Error())
		buf.WriteString("\n```\n\n")
		if *runURL != "" {
			fmt.Fprintf(&buf, ":file_folder: [Workflow logs](%s)\n\n", *runURL)
		}
		buf.WriteString("</details>\n\n")
		entries = nil
	}
	sort.Strings(entries)
	for _, dir := range entries {
		info, err := os.Stat(dir)
		if err != nil || !info.IsDir() {
			continue
		}
		label := strings.TrimPrefix(info.Name(), "benchstat-")
		// label comes from artifact directory names supplied by the caller and
		// is interpolated into HTML below; escape to keep the comment valid even
		// if a future caller uses unusual characters.
		safeLabel := html.EscapeString(label)
		jobURL := jobURLs[label]
		if jobURL == "" {
			jobURL = *runURL
		}
		status := readJobStatus(filepath.Join(dir, "status.json"))
		failures := readFileContent(filepath.Join(dir, "failures.txt"))
		regressions := readFileContent(filepath.Join(dir, "regressions.txt"))

		if status.Failed() {
			buf.WriteString("<details>\n")
			fmt.Fprintf(&buf, "<summary>:x: <code>%s</code></summary>\n\n", safeLabel)
			if status.BenchmarkError {
				fmt.Fprintf(&buf, ":boom: **Benchmark run failed** — see [workflow logs](%s) for details.\n\n", jobURL)
			}
			if failures != "" {
				buf.WriteString("**Test failures:**\n```\n")
				buf.WriteString(failures)
				buf.WriteString("\n```\n\n")
			}
			if regressions != "" {
				buf.WriteString("**Regressions:**\n```\n")
				buf.WriteString(regressions)
				buf.WriteString("\n```\n\n")
			} else if !status.BenchmarkError {
				buf.WriteString("No benchmark regressions detected.\n\n")
			}
			fmt.Fprintf(&buf, ":file_folder: [Full results](%s)\n\n", jobURL)
			buf.WriteString("</details>\n\n")
		} else {
			fmt.Fprintf(&buf, ":white_check_mark: `%s` · [results](%s)\n", safeLabel, jobURL)
		}
	}

	// Truncate if needed to fit GitHub's PR comment limit.
	output := buf.String()
	if len(output) > *maxLen {
		truncMsg := "\n\n---\n:scissors: **Report truncated** — see workflow artifacts for full results.\n"
		cut := *maxLen - len(truncMsg)
		if cut < 0 {
			cut = 0
		}
		output = output[:cut] + truncMsg
	}

	fmt.Print(output)
}

func readJobStatus(path string) Status {
	data, err := os.ReadFile(path)
	if err != nil {
		if !errors.Is(err, os.ErrNotExist) {
			fmt.Fprintf(os.Stderr, "reading %s: %v\n", path, err)
		}
		// Missing or unreadable status file means the job errored before writing it.
		return Status{BenchmarkError: true}
	}
	var s Status
	if err := json.Unmarshal(data, &s); err != nil {
		fmt.Fprintf(os.Stderr, "parsing %s: %v\n", path, err)
		return Status{BenchmarkError: true}
	}
	return s
}

func loadJobURLs(path string) map[string]string {
	urls := make(map[string]string)
	if path == "" {
		return urls
	}
	data, err := os.ReadFile(path)
	if err != nil {
		if !errors.Is(err, os.ErrNotExist) {
			fmt.Fprintf(os.Stderr, "reading %s: %v\n", path, err)
		}
		return urls
	}
	for _, line := range strings.Split(strings.TrimSpace(string(data)), "\n") {
		parts := strings.SplitN(line, "\t", 2)
		if len(parts) == 2 {
			urls[parts[0]] = parts[1]
		}
	}
	return urls
}

func readFileContent(path string) string {
	data, err := os.ReadFile(path)
	if err != nil {
		if !errors.Is(err, os.ErrNotExist) {
			fmt.Fprintf(os.Stderr, "reading %s: %v\n", path, err)
		}
		return ""
	}
	return strings.TrimSpace(string(data))
}
