// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package main

import (
	"bufio"
	"encoding/json"
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

	scanner := bufio.NewScanner(r)
	// Benchmark output and panic traces can produce very long lines; raise the
	// limit well above the 64KiB default so Scan does not bail out with
	// ErrTooLong partway through.
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)

	var failures []string
	for scanner.Scan() {
		var e testEvent
		if err := json.Unmarshal(scanner.Bytes(), &e); err != nil {
			continue // not a JSON event; ignore
		}
		key := scopeKey{e.Package, e.Test}
		switch e.Action {
		case "output":
			bench.WriteString(e.Output)
			buffered[key] = append(buffered[key], e.Output)
		case "pass", "skip":
			// The scope succeeded; its output is not a failure.
			delete(buffered, key)
		case "fail":
			for _, line := range failureContext(buffered[key]) {
				failures = append(failures, prefix+line)
			}
			delete(buffered, key)
		}
	}
	return testJSON{BenchText: bench.String(), Failures: failures}, scanner.Err()
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
