// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package main

import (
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"testing"
)

// benchLines generates n identical benchmark result lines.
func benchLines(name string, n int, nsPerOp float64, bPerOp, allocsPerOp int) string {
	var b strings.Builder
	for i := 0; i < n; i++ {
		fmt.Fprintf(&b, "%s\t1\t%.1f ns/op\t%d B/op\t%d allocs/op\n",
			name, nsPerOp, bPerOp, allocsPerOp)
	}
	return b.String()
}

func parse(t *testing.T, text string) map[benchKey][]float64 {
	t.Helper()
	return parseBenchmarks(strings.NewReader(text), "test.txt")
}

func defaultCfg() config {
	return config{
		TimeThreshold: 5,
		MinTime:       1e-6, // 1µs
		Alpha:         0.05,
	}
}

func TestNoRegression(t *testing.T) {
	data := benchLines("BenchmarkFoo-8", 10, 5000, 64, 2)
	base := parse(t, data)
	head := parse(t, data)
	regressions := checkRegressions(base, head, defaultCfg())
	if len(regressions) != 0 {
		t.Errorf("expected no regressions, got %d: %+v", len(regressions), regressions)
	}
}

func TestAllocRegression_BPerOp(t *testing.T) {
	base := parse(t, benchLines("BenchmarkFoo-8", 10, 5000, 64, 2))
	head := parse(t, benchLines("BenchmarkFoo-8", 10, 5000, 128, 2))
	regressions := checkRegressions(base, head, defaultCfg())
	found := false
	for _, r := range regressions {
		if r.Unit == "B/op" {
			found = true
		}
	}
	if !found {
		t.Error("expected B/op regression, got none")
	}
}

func TestAllocRegression_AllocsPerOp(t *testing.T) {
	base := parse(t, benchLines("BenchmarkFoo-8", 10, 5000, 64, 2))
	head := parse(t, benchLines("BenchmarkFoo-8", 10, 5000, 64, 4))
	regressions := checkRegressions(base, head, defaultCfg())
	found := false
	for _, r := range regressions {
		if r.Unit == "allocs/op" {
			found = true
		}
	}
	if !found {
		t.Error("expected allocs/op regression, got none")
	}
}

func TestTimeRegression_AboveThreshold(t *testing.T) {
	// 5000 ns = 5µs (above 1µs minimum), 10% regression (above 5% threshold)
	base := parse(t, benchLines("BenchmarkFoo-8", 10, 5000, 0, 0))
	head := parse(t, benchLines("BenchmarkFoo-8", 10, 5500, 0, 0))
	regressions := checkRegressions(base, head, defaultCfg())
	found := false
	for _, r := range regressions {
		if r.Unit == "sec/op" {
			found = true
		}
	}
	if !found {
		t.Error("expected sec/op regression, got none")
	}
}

func TestTimeRegression_BelowMinTime(t *testing.T) {
	// 50 ns (below 1µs minimum) — should NOT flag even with large % increase
	base := parse(t, benchLines("BenchmarkFoo-8", 10, 50, 0, 0))
	head := parse(t, benchLines("BenchmarkFoo-8", 10, 100, 0, 0))
	regressions := checkRegressions(base, head, defaultCfg())
	for _, r := range regressions {
		if r.Unit == "sec/op" {
			t.Errorf("unexpected sec/op regression for sub-µs benchmark: %+v", r)
		}
	}
}

func TestTimeRegression_BelowPctThreshold(t *testing.T) {
	// 5000 ns = 5µs, but only 2% regression (below 5% threshold)
	base := parse(t, benchLines("BenchmarkFoo-8", 10, 5000, 0, 0))
	head := parse(t, benchLines("BenchmarkFoo-8", 10, 5100, 0, 0))
	regressions := checkRegressions(base, head, defaultCfg())
	for _, r := range regressions {
		if r.Unit == "sec/op" {
			t.Errorf("unexpected sec/op regression for <5%% change: %+v", r)
		}
	}
}

func TestImprovement_NotFlagged(t *testing.T) {
	// Head is faster — should NOT flag
	base := parse(t, benchLines("BenchmarkFoo-8", 10, 5000, 64, 2))
	head := parse(t, benchLines("BenchmarkFoo-8", 10, 4000, 32, 1))
	regressions := checkRegressions(base, head, defaultCfg())
	if len(regressions) != 0 {
		t.Errorf("expected no regressions for improvement, got %d: %+v",
			len(regressions), regressions)
	}
}

func TestZeroBaseAlloc(t *testing.T) {
	// Base has 0 B/op, head has non-zero
	base := parse(t, benchLines("BenchmarkFoo-8", 10, 5000, 0, 0))
	head := parse(t, benchLines("BenchmarkFoo-8", 10, 5000, 64, 2))
	regressions := checkRegressions(base, head, defaultCfg())
	allocFound := false
	for _, r := range regressions {
		if isAllocUnit(r.Unit) {
			allocFound = true
		}
	}
	if !allocFound {
		t.Error("expected allocation regression for 0→non-zero, got none")
	}
}

func TestCustomThresholds(t *testing.T) {
	// 10% time threshold — 8% regression should NOT flag
	base := parse(t, benchLines("BenchmarkFoo-8", 10, 5000, 0, 0))
	head := parse(t, benchLines("BenchmarkFoo-8", 10, 5400, 0, 0))
	cfg := config{
		TimeThreshold: 10,
		MinTime:       1e-6,
		Alpha:         0.05,
	}
	regressions := checkRegressions(base, head, cfg)
	for _, r := range regressions {
		if r.Unit == "sec/op" {
			t.Errorf("unexpected sec/op regression with 10%% threshold: %+v", r)
		}
	}
}

func TestParseBenchmarks(t *testing.T) {
	input := "BenchmarkSHA256-8\t1000\t1234 ns/op\t56 B/op\t3 allocs/op\n" +
		"BenchmarkSHA256-8\t1000\t1245 ns/op\t56 B/op\t3 allocs/op\n" +
		"BenchmarkAES-8\t1000\t567 ns/op\t0 B/op\t0 allocs/op\n"
	values := parse(t, input)

	// benchfmt strips the "Benchmark" prefix from names.
	sha256Time := values[benchKey{Name: "SHA256-8", Unit: "sec/op"}]
	if len(sha256Time) != 2 {
		t.Fatalf("expected 2 sec/op values for SHA256, got %d", len(sha256Time))
	}
	// 1234 ns = 1.234e-6 sec
	if sha256Time[0] < 1e-7 || sha256Time[0] > 1e-5 {
		t.Errorf("unexpected sec/op value: %g", sha256Time[0])
	}

	sha256Alloc := values[benchKey{Name: "SHA256-8", Unit: "B/op"}]
	if len(sha256Alloc) != 2 {
		t.Fatalf("expected 2 B/op values for SHA256, got %d", len(sha256Alloc))
	}
}

func TestParseTestJSON_TestFailure(t *testing.T) {
	// A failing test: its output (minus the "=== RUN" framing) is captured; the
	// package-level FAIL summary is captured too. Benchmark output from a passing
	// package is not treated as a failure.
	input := strings.Join([]string{
		`{"Action":"run","Package":"ex","Test":"TestFoo"}`,
		`{"Action":"output","Package":"ex","Test":"TestFoo","Output":"=== RUN   TestFoo\n"}`,
		`{"Action":"output","Package":"ex","Test":"TestFoo","Output":"    foo_test.go:42: expected 1, got 2\n"}`,
		`{"Action":"output","Package":"ex","Test":"TestFoo","Output":"--- FAIL: TestFoo (0.01s)\n"}`,
		`{"Action":"fail","Package":"ex","Test":"TestFoo"}`,
		`{"Action":"output","Package":"ex","Output":"FAIL\tex\t0.123s\n"}`,
		`{"Action":"fail","Package":"ex"}`,
	}, "\n") + "\n"

	got, err := parseTestJSON(strings.NewReader(input), "head: ")
	if err != nil {
		t.Fatal(err)
	}
	want := []string{
		"head:     foo_test.go:42: expected 1, got 2",
		"head: --- FAIL: TestFoo (0.01s)",
		"head: FAIL\tex\t0.123s",
	}
	if !slices.Equal(got.Failures, want) {
		t.Errorf("failures = %#v, want %#v", got.Failures, want)
	}
}

func TestParseTestJSON_BuildFailure(t *testing.T) {
	input := strings.Join([]string{
		`{"Action":"output","Package":"ex","Output":"# ex\n"}`,
		`{"Action":"output","Package":"ex","Output":"foo.go:3:2: undefined: bar\n"}`,
		`{"Action":"output","Package":"ex","Output":"FAIL\tex [build failed]\n"}`,
		`{"Action":"fail","Package":"ex"}`,
	}, "\n") + "\n"

	got, err := parseTestJSON(strings.NewReader(input), "")
	if err != nil {
		t.Fatal(err)
	}
	want := []string{
		"# ex",
		"foo.go:3:2: undefined: bar",
		"FAIL\tex [build failed]",
	}
	if !slices.Equal(got.Failures, want) {
		t.Errorf("failures = %#v, want %#v", got.Failures, want)
	}
}

func TestParseTestJSON_Crash(t *testing.T) {
	input := strings.Join([]string{
		`{"Action":"output","Package":"ex","Test":"TestFoo","Output":"panic: runtime error: index out of range\n"}`,
		`{"Action":"output","Package":"ex","Test":"TestFoo","Output":"goroutine 1 [running]:\n"}`,
		`{"Action":"fail","Package":"ex","Test":"TestFoo"}`,
		`{"Action":"fail","Package":"ex"}`,
	}, "\n") + "\n"

	got, err := parseTestJSON(strings.NewReader(input), "")
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Failures) == 0 || !strings.HasPrefix(got.Failures[0], "panic:") {
		t.Errorf("expected panic line first, got %#v", got.Failures)
	}
}

func TestParseTestJSON_NoFailures(t *testing.T) {
	// A passing benchmark run: reconstructed text feeds benchfmt, no failures.
	input := strings.Join([]string{
		`{"Action":"run","Package":"ex","Test":"BenchmarkFoo"}`,
		`{"Action":"output","Package":"ex","Output":"BenchmarkFoo-8\t1000\t1234 ns/op\t56 B/op\t3 allocs/op\n"}`,
		`{"Action":"output","Package":"ex","Output":"PASS\n"}`,
		`{"Action":"output","Package":"ex","Output":"ok\tex\t1.234s\n"}`,
		`{"Action":"pass","Package":"ex"}`,
	}, "\n") + "\n"

	got, err := parseTestJSON(strings.NewReader(input), "")
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Failures) != 0 {
		t.Errorf("expected no failures, got %#v", got.Failures)
	}
	want := "BenchmarkFoo-8\t1000\t1234 ns/op\t56 B/op\t3 allocs/op\nPASS\nok\tex\t1.234s\n"
	if got.BenchText != want {
		t.Errorf("BenchText = %q, want %q", got.BenchText, want)
	}
	// The reconstructed text must parse as benchmark results.
	values := parse(t, got.BenchText)
	if len(values[benchKey{Name: "Foo-8", Unit: "sec/op"}]) != 1 {
		t.Errorf("expected 1 sec/op value, got values %v", values)
	}
}

func TestParseTestJSON_NonJSONTolerated(t *testing.T) {
	// Stray non-JSON lines (e.g. tool diagnostics) are skipped, not fatal.
	input := "not json at all\n" +
		`{"Action":"output","Package":"ex","Output":"BenchmarkFoo-8\t10\t5 ns/op\n"}` + "\n" +
		"another stray line\n" +
		`{"Action":"pass","Package":"ex"}` + "\n"

	got, err := parseTestJSON(strings.NewReader(input), "")
	if err != nil {
		t.Fatal(err)
	}
	if got.BenchText != "BenchmarkFoo-8\t10\t5 ns/op\n" {
		t.Errorf("BenchText = %q", got.BenchText)
	}
	if len(got.Failures) != 0 {
		t.Errorf("expected no failures, got %#v", got.Failures)
	}
}

func TestParseTestJSON_LongLine(t *testing.T) {
	// A single event larger than bufio.Scanner's fixed token limit must not
	// abort parsing and drop the events that follow it.
	huge := strings.Repeat("x", 4*1024*1024)
	blob, err := json.Marshal(testEvent{Action: "output", Package: "ex", Test: "TestBig", Output: huge + "\n"})
	if err != nil {
		t.Fatal(err)
	}
	input := string(blob) + "\n" +
		`{"Action":"fail","Package":"ex","Test":"TestBig"}` + "\n" +
		`{"Action":"output","Package":"ex","Output":"FAIL\tex\t0.10s\n"}` + "\n" +
		`{"Action":"fail","Package":"ex"}` + "\n"

	got, err := parseTestJSON(strings.NewReader(input), "")
	if err != nil {
		t.Fatal(err)
	}
	// The failure after the huge line must still be captured.
	if !slices.Contains(got.Failures, "FAIL\tex\t0.10s") {
		t.Errorf("expected the trailing package FAIL line to be captured, got %#v",
			sliceHead(got.Failures))
	}
}

// sliceHead returns a copy of s truncated for readable test failure messages.
func sliceHead(s []string) []string {
	const max = 5
	if len(s) > max {
		return s[:max]
	}
	return s
}
