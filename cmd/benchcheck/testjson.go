// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package main

import (
	"bufio"
	"encoding/json"
	"errors"
	"io"
	"os"
	"strings"
)

// testEvent is a single `go test -json` (test2json) event. Only the fields
// benchcheck needs are decoded.
type testEvent struct {
	Action  string
	Package string
	Test    string
	Output  string
}

// testJSON holds the result of parsing a `go test -json` stream.
type testJSON struct {
	// BenchText is the reconstructed plain-text test output: every output event
	// concatenated in order. It is what benchstat and benchfmt consume.
	BenchText string
	// Failures are human-readable lines describing build errors, failed tests,
	// and crashes, each prefixed with the caller-supplied prefix.
	Failures []string
}

// scopeKey identifies the package (and optionally test) an output event belongs
// to, so buffered output can be attributed to the thing that ultimately fails.
type scopeKey struct {
	pkg  string
	test string
}

// parseTestJSONFile opens path and parses its `go test -json` contents.
func parseTestJSONFile(path, prefix string) (testJSON, error) {
	f, err := os.Open(path)
	if err != nil {
		return testJSON{}, err
	}
	defer f.Close()
	return parseTestJSON(f, prefix)
}

// parseTestJSON reads a `go test -json` stream and returns the reconstructed
// plain-text output plus any extracted failure lines. Failures are detected
// structurally from "fail" events rather than by pattern-matching text, and the
// output captured for the failing package or test is included for context.
// Non-JSON lines (e.g. stray tool output) are tolerated and skipped.
func parseTestJSON(r io.Reader, prefix string) (testJSON, error) {
	var bench strings.Builder
	// Buffer output per scope until we learn whether it passed or failed.
	// Passing scopes are dropped to keep memory bounded; failing scopes are
	// flushed into the failure list.
	buffered := make(map[scopeKey][]string)

	// Read line by line with a bufio.Reader rather than a bufio.Scanner: a
	// single test2json event (e.g. an Output field carrying a very long panic
	// or log line) can exceed Scanner's fixed token limit, which would abort
	// parsing partway through and silently drop later failures and benchmark
	// output. ReadBytes grows as needed, bounded only by one line's length.
	br := bufio.NewReader(r)

	var failures []string
	for {
		line, readErr := br.ReadBytes('\n')
		if len(line) > 0 {
			var e testEvent
			if err := json.Unmarshal(line, &e); err == nil {
				key := scopeKey{e.Package, e.Test}
				switch e.Action {
				case "output":
					bench.WriteString(e.Output)
					buffered[key] = append(buffered[key], e.Output)
				case "pass", "skip":
					// The scope succeeded; its output is not a failure.
					delete(buffered, key)
				case "fail":
					for _, l := range failureContext(buffered[key]) {
						failures = append(failures, prefix+l)
					}
					delete(buffered, key)
				}
			}
			// A line that does not parse as a JSON event (e.g. stray tool
			// output) is tolerated and skipped.
		}
		if readErr != nil {
			if errors.Is(readErr, io.EOF) {
				break
			}
			return testJSON{BenchText: bench.String(), Failures: failures}, readErr
		}
	}
	return testJSON{BenchText: bench.String(), Failures: failures}, nil
}

// failureContext turns a failing scope's raw output events into concise,
// meaningful lines: it splits on newlines, drops blank lines and the "=== "
// framing lines (RUN/PAUSE/CONT/NAME) that carry no failure information, and
// keeps everything else (build errors, "--- FAIL" details, panics, logs).
func failureContext(output []string) []string {
	var lines []string
	for _, chunk := range output {
		for _, line := range strings.Split(chunk, "\n") {
			line = strings.TrimRight(line, "\r")
			if strings.TrimSpace(line) == "" {
				continue
			}
			if strings.HasPrefix(line, "=== ") {
				continue
			}
			lines = append(lines, line)
		}
	}
	return lines
}
