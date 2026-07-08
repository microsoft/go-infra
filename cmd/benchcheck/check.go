// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"golang.org/x/perf/benchfmt"
	"golang.org/x/perf/benchmath"
)

type benchKey struct {
	Name string
	Unit string
}

type config struct {
	TimeThreshold float64 // minimum sec/op regression percentage
	MinTime       float64 // minimum base time in seconds
	Alpha         float64 // significance level
}

type regression struct {
	Name       string
	Unit       string
	PctChange  float64
	PValue     float64
	BaseVal    float64
	BaseSpread string // base confidence-interval range, e.g. "3%"
	HeadSpread string // head confidence-interval range, e.g. "3%"
}

func cmdCheck(args []string) {
	fs := flag.NewFlagSet("check", flag.ExitOnError)
	timeThreshold := fs.Float64("time-threshold", 50, "minimum sec/op regression percentage to flag")
	minTime := fs.Duration("min-time", time.Microsecond, "minimum base time for sec/op checks")
	alpha := fs.Float64("alpha", 0.05, "significance level for statistical tests")
	regressionsOut := fs.String("o-regressions", "", "output file for regression details (skipped if empty)")
	failuresOut := fs.String("o-failures", "", "output file for test failure details (skipped if empty)")
	statusOut := fs.String("o-status", "", "output file for machine-readable status JSON (skipped if empty)")
	baseTextOut := fs.String("o-base-text", "", "output file for reconstructed base benchmark text, for benchstat (skipped if empty)")
	headTextOut := fs.String("o-head-text", "", "output file for reconstructed head benchmark text, for benchstat (skipped if empty)")
	fs.Usage = func() {
		fmt.Fprintf(os.Stderr, `usage: benchcheck check [flags] base.json head.json

Compare base and head benchmark results, detect regressions and test failures.
The inputs are `+"`go test -json`"+` (test2json) output files.

Writes the following output files (only if the corresponding flag is set):
  -o-regressions  One-line summary per detected regression.
  -o-failures     Extracted build errors, test failures, and crash traces.
  -o-status       Machine-readable JSON status for the report subcommand.
  -o-base-text    Reconstructed plain-text base output, for benchstat.
  -o-head-text    Reconstructed plain-text head output, for benchstat.

Exits 0 if no issues are found, 1 if regressions, failures, or write errors occur.

Flags:
`)
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		os.Exit(2)
	}

	if fs.NArg() != 2 {
		fs.Usage()
		os.Exit(2)
	}

	cfg := config{
		TimeThreshold: *timeThreshold,
		MinTime:       minTime.Seconds(),
		Alpha:         *alpha,
	}

	basePath, headPath := fs.Arg(0), fs.Arg(1)

	// Parse the test2json inputs once each: this yields both the extracted
	// failures and the reconstructed plain-text benchmark output.
	var benchError bool
	base, err := parseTestJSONFile(basePath, "base: ")
	if err != nil {
		fmt.Fprintf(os.Stderr, "reading %s: %v\n", basePath, err)
		benchError = true
	}
	head, err := parseTestJSONFile(headPath, "head: ")
	if err != nil {
		fmt.Fprintf(os.Stderr, "reading %s: %v\n", headPath, err)
		benchError = true
	}

	failureLines := append(append([]string{}, base.Failures...), head.Failures...)
	hasFailures := len(failureLines) > 0

	// Parse benchmarks from the reconstructed text and check regressions.
	baseValues := parseBenchmarks(strings.NewReader(base.BenchText), basePath)
	headValues := parseBenchmarks(strings.NewReader(head.BenchText), headPath)
	regressions := checkRegressions(baseValues, headValues, cfg)
	hasRegressions := len(regressions) > 0

	// Sort regressions by percentage descending.
	sort.Slice(regressions, func(i, j int) bool {
		return regressions[i].PctChange > regressions[j].PctChange
	})

	// Write regressions.txt.
	var regressionLines []string
	for _, r := range regressions {
		if isAllocUnit(r.Unit) {
			regressionLines = append(regressionLines, fmt.Sprintf("alloc regression: %s [%s] +%.2f%% (p=%.3f, base %s, head %s)", r.Name, r.Unit, r.PctChange, r.PValue, r.BaseSpread, r.HeadSpread))
		} else {
			regressionLines = append(regressionLines, fmt.Sprintf("time regression: %s +%.2f%% (p=%.3f, base=%.2g sec %s, head %s)", r.Name, r.PctChange, r.PValue, r.BaseVal, r.BaseSpread, r.HeadSpread))
		}
	}
	writeErr := errors.Join(
		writeTextFile(*baseTextOut, base.BenchText),
		writeTextFile(*headTextOut, head.BenchText),
		writeLines(*regressionsOut, regressionLines),
		writeLines(*failuresOut, failureLines),
		writeStatus(*statusOut, Status{
			Regression:     hasRegressions,
			TestFailures:   hasFailures,
			BenchmarkError: benchError,
		}),
	)
	if writeErr != nil {
		fmt.Printf("::error::Failed to write benchcheck output: %v\n", writeErr)
	}

	// Print summary with GitHub Actions annotations.
	if hasRegressions {
		fmt.Println("::error::Benchmark regression detected — see benchstat output above for details.")
		fmt.Println()
		fmt.Println("=== Regressions ===")
		for _, line := range regressionLines {
			fmt.Println(line)
		}
	}
	if hasFailures {
		fmt.Println("::error::Test failure detected — see the 'Run benchmarks' steps for details.")
		fmt.Println()
		fmt.Println("=== Test failures ===")
		for _, line := range failureLines {
			fmt.Println(line)
		}
	}
	if benchError {
		fmt.Println("::error::Failed to read benchmark results — see logs above.")
	}
	if !hasRegressions && !hasFailures && !benchError && writeErr == nil {
		fmt.Println("No benchmark regressions or test failures detected.")
	}
	if hasRegressions || hasFailures || benchError || writeErr != nil {
		os.Exit(1)
	}
}

func parseBenchmarks(r io.Reader, name string) map[benchKey][]float64 {
	result := make(map[benchKey][]float64)
	reader := benchfmt.NewReader(r, name)
	for reader.Scan() {
		rec := reader.Result()
		res, ok := rec.(*benchfmt.Result)
		if !ok {
			continue
		}
		benchName := res.Name.String()
		for _, v := range res.Values {
			key := benchKey{Name: benchName, Unit: v.Unit}
			result[key] = append(result[key], v.Value)
		}
	}
	return result
}

func checkRegressions(base, head map[benchKey][]float64, cfg config) []regression {
	thresholds := &benchmath.Thresholds{CompareAlpha: cfg.Alpha}
	var regressions []regression

	for key, baseVals := range base {
		headVals, ok := head[key]
		if !ok {
			continue
		}

		baseSample := benchmath.NewSample(baseVals, thresholds)
		headSample := benchmath.NewSample(headVals, thresholds)

		cmp := benchmath.AssumeNothing.Compare(baseSample, headSample)
		if cmp.P >= cmp.Alpha {
			continue // not statistically significant
		}

		baseSummary := benchmath.AssumeNothing.Summary(baseSample, 0.95)
		headSummary := benchmath.AssumeNothing.Summary(headSample, 0.95)

		// Record each sample's confidence-interval range so the report can show
		// how noisy the benchmark is.
		baseSpread := baseSummary.PctRangeString()
		headSpread := headSummary.PctRangeString()

		// Compare the least-performant base (upper CI bound) against the
		// best-performing head (lower CI bound), flagging a regression only when
		// the confidence intervals don't overlap. For zero-variance samples this
		// reduces to comparing Centers.
		baseWorst := baseSummary.Hi
		headBest := headSummary.Lo

		if headBest <= baseWorst {
			continue // improvement, no change, or within variance
		}

		if baseWorst == 0 {
			// Base upper bound is zero; only flag allocation units (sec/op can't be 0 meaningfully).
			if isAllocUnit(key.Unit) {
				regressions = append(regressions, regression{
					Name:       key.Name,
					Unit:       key.Unit,
					PctChange:  100, // 0 → non-zero
					PValue:     cmp.P,
					BaseSpread: baseSpread,
					HeadSpread: headSpread,
				})
			}
			continue
		}

		pctChange := (headBest - baseWorst) / baseWorst * 100

		switch {
		case isAllocUnit(key.Unit):
			regressions = append(regressions, regression{
				Name:       key.Name,
				Unit:       key.Unit,
				PctChange:  pctChange,
				PValue:     cmp.P,
				BaseSpread: baseSpread,
				HeadSpread: headSpread,
			})
		case key.Unit == "sec/op":
			if baseSummary.Center >= cfg.MinTime && pctChange >= cfg.TimeThreshold {
				regressions = append(regressions, regression{
					Name:       key.Name,
					Unit:       key.Unit,
					PctChange:  pctChange,
					PValue:     cmp.P,
					BaseVal:    baseSummary.Center,
					BaseSpread: baseSpread,
					HeadSpread: headSpread,
				})
			}
		}
	}

	return regressions
}

func isAllocUnit(unit string) bool {
	return unit == "B/op" || unit == "allocs/op"
}

func writeLines(path string, lines []string) error {
	if path == "" {
		return nil
	}
	var data []byte
	if len(lines) > 0 {
		data = []byte(strings.Join(lines, "\n") + "\n")
	}
	return writeFile(path, data)
}

// writeTextFile writes content verbatim to path, skipping if path is empty.
func writeTextFile(path, content string) error {
	if path == "" {
		return nil
	}
	return writeFile(path, []byte(content))
}

// Status is the machine-readable status written by check and read by report.
type Status struct {
	Regression     bool `json:"regression"`
	TestFailures   bool `json:"test_failures"`
	BenchmarkError bool `json:"benchmark_error"`
}

// Failed reports whether any failure flag is set.
func (s Status) Failed() bool {
	return s.Regression || s.TestFailures || s.BenchmarkError
}

func writeStatus(path string, s Status) error {
	if path == "" {
		return nil
	}
	data, err := json.Marshal(s)
	if err != nil {
		return fmt.Errorf("marshaling status: %w", err)
	}
	return writeFile(path, data)
}

func writeFile(path string, data []byte) error {
	if dir := filepath.Dir(path); dir != "" && dir != "." {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return fmt.Errorf("creating directory for %s: %w", path, err)
		}
	}
	if err := os.WriteFile(path, data, 0o644); err != nil {
		return fmt.Errorf("writing %s: %w", path, err)
	}
	return nil
}
