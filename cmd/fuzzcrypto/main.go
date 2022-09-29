// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package main

import (
	"flag"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"
)

const description = `
fuzzcrypto runs a curated list of fuzz tests sequentially.

Example that runs all the available fuzz tests:

  go run .

Example that runs only the go-cose subset for 1 minute:

  go run . -run go-cose -fuzztime 1m

The total fuzzing time, set by -fuzztime, is distributed among the executed fuzz tests.
fuzzcrypto might run longer than -fuzztime as it does not take into account build time
nor the fuzz seeding time.`

const helpFuzztime = `Run enough iterations of all the fuzz targets during fuzzing to take t,
specified as a time.Duration (for example, -fuzztime 1h30s).
	The default is 5m.
The special syntax Nx means to run each fuzz target N times
(for example, -fuzztime 1000x).`

const helpRun = `Run only those fuzz targets matching the regular expression.`

const defaultFuzzTime = 5 * time.Minute

func main() {
	var verbose = flag.Bool("v", false, "Verbose output.")
	var fuzzDuration durationOrCountFlag
	flag.Var(&fuzzDuration, "fuzztime", helpFuzztime)
	var run = flagRegex("run", helpRun)
	flag.Usage = func() {
		fmt.Fprintf(flag.CommandLine.Output(), "\nUsage:\n")
		flag.PrintDefaults()
		fmt.Fprintf(flag.CommandLine.Output(), "%s\n\n", description)
	}
	flag.Parse()
	if fuzzDuration.d == 0 && fuzzDuration.n == 0 {
		fuzzDuration.d = defaultFuzzTime
	}

	targets := filterTargets(*run)

	var sumweights float64
	for _, t := range targets {
		sumweights += t.weight
	}

	var errs []string
	for i, t := range targets {
		var targetDuration durationOrCountFlag
		if fuzzDuration.n > 0 {
			targetDuration.n = fuzzDuration.n
		} else {
			targetDuration.d = time.Duration((t.weight / sumweights) * float64(fuzzDuration.d))
		}
		log.Printf("Running fuzz target %s for %v. %d/%d completed\n", t.name, targetDuration, i, len(targets))

		err := fuzz(t.name, targetDuration, *verbose)
		if err != nil {
			if _, ok := err.(*exec.ExitError); !ok {
				errs = append(errs, fmt.Sprintf("fuzz target %q can't be executed: %v", t.name, err))
			} else {
				errs = append(errs, fmt.Sprintf("fuzz target %q failed: %v", t.name, err))
			}
		}
	}
	if len(errs) > 0 {
		for _, err := range errs {
			fmt.Println(err)
		}
		os.Exit(1)
	}
}

func flagRegex(name, usage string) **regexp.Regexp {
	var reg *regexp.Regexp
	flag.Func(name, usage, func(s string) (err error) {
		reg, err = regexp.Compile(s)
		return err
	})
	return &reg
}

// fuzz executes the named fuzz test.
// It only returns an error if the test binary could not be executed.
func fuzz(name string, d durationOrCountFlag, verbose bool) error {
	dir, fuzzname := path.Split(name)
	cmd := exec.Command("go", "test",
		"-run", "-", // don't run any normal test
		"-fuzztime", d.String(),
		"-fuzz", "^"+fuzzname+"$", // ensure we are strictly matching name
	)
	cmd.Dir = filepath.Join(".", dir)
	if verbose {
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr
	}
	return cmd.Run()
}

// filterTargets returns the list of fuzz targets to execute,
// which is either equal or a subset of alltargets.
func filterTargets(run *regexp.Regexp) []target {
	if run == nil {
		return alltargets
	}
	targets := make([]target, 0, len(alltargets))
	for _, t := range alltargets {
		if run.MatchString(t.name) {
			targets = append(targets, t)
		}
	}
	return targets
}

// durationOrCountFlag can either contain a
// duration or a number, not both.
// It implements the flag.Value interface.
type durationOrCountFlag struct {
	d time.Duration
	n int
}

func (f durationOrCountFlag) String() string {
	if f.n > 0 {
		return fmt.Sprintf("%dx", f.n)
	}
	return f.d.String()
}

func (f *durationOrCountFlag) Set(s string) error {
	if strings.HasSuffix(s, "x") {
		n, err := strconv.ParseInt(s[:len(s)-1], 10, 0)
		if err != nil || n <= 0 {
			return fmt.Errorf("invalid count")
		}
		*f = durationOrCountFlag{n: int(n)}
		return nil
	}
	d, err := time.ParseDuration(s)
	if err != nil || d <= 0 {
		return fmt.Errorf("invalid duration")
	}
	*f = durationOrCountFlag{d: d}
	return nil
}
