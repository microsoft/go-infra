// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package main

import (
	"container/heap"
	"flag"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"regexp"
	"sort"
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
nor the fuzz seeding time.

The bucket arg, if passed, discretely divides the set of targets into buckets. Because each target
is put into only one bucket, time may not be distributed perfectly evenly. Target weight is taken
into account with a simple greedy algorithm: the highest weight is repeatedly taken out of the full
set of targets and added to the bucket with the lowest sum of weights until the working set is
empty. If -run and -bucket are both specified, -run is applied first.

The targets are specified in 'targets.go'. The order of targets in that file is respected by the
-run and -bucket behavior.`

const helpFuzztime = `Run enough iterations of all the fuzz targets during fuzzing to take t,
specified as a time.Duration (for example, -fuzztime 1h30s).
	The default is 5m.
The special syntax Nx means to run each fuzz target N times
(for example, -fuzztime 1000x).`

const helpBucket = "The 1-indexed bucket B out of total buckets N, passed as a string 'B/N'."

const helpRun = `Run only those fuzz targets matching the regular expression.`

const defaultFuzzTime = 5 * time.Minute

func main() {
	verbose := flag.Bool("v", false, "Verbose output.")
	var fuzzDuration durationOrCountFlag
	flag.Var(&fuzzDuration, "fuzztime", helpFuzztime)
	run := flagRegex("run", helpRun)
	bucket, bucketCount := flagBucket("bucket", helpBucket)
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
	if *bucketCount > 1 {
		targets = bucketTargets(targets, *bucket, *bucketCount)
		if len(targets) == 0 {
			log.Printf("Warning: No work to do in this bucket. Bucket count may be too high.")
		}
	}

	log.Printf("Running targets: %v\n", targetNames(targets))

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

func flagBucket(name, usage string) (bucket, bucketCount *int) {
	var b, c int
	flag.Func(name, usage, func(s string) error {
		before, after, found := strings.Cut(s, "/")
		if !found {
			return fmt.Errorf("no '/' found in %q", s)
		}

		var err error
		b, err = strconv.Atoi(before)
		if err != nil {
			return fmt.Errorf("string before '/' is not a number: %v", err)
		}
		c, err = strconv.Atoi(after)
		if err != nil {
			return fmt.Errorf("string after '/' is not a number: %v", err)
		}

		if b < 1 || b > c {
			return fmt.Errorf("bucket %v is not in interval [1, %v]", b, c)
		}
		return nil
	})
	return &b, &c
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

// bucketTargets puts each target into bucketCount buckets and returns the targets in the 1-indexed
// bucket specified by "bucket". The order of targets in each bucket is the same as the order of
// those targets in the all slice.
func bucketTargets(all []target, bucket, bucketCount int) []target {
	// Copy all slice so we can sort it without affecting the original order.
	targets := append([]target(nil), all...)

	// Repeatedly add the largest target to the emptiest bucket. This somewhat evens out the buckets
	// in a way that respects the weight.
	sort.Slice(targets, func(i, j int) bool {
		return targets[i].weight > targets[j].weight
	})
	buckets := make(targetBucketHeap, bucketCount)
	heap.Init(&buckets)
	for _, t := range targets {
		b := &buckets[0]
		heap.Fix(&buckets, 0)
		b.totalWeight += t.weight
		b.targets = append(b.targets, t)
	}
	targets = buckets[bucket-1].targets

	// For diag: print all buckets.
	for i, b := range buckets {
		log.Printf("Bucket %d (%f): %v\n", i+1, b.totalWeight, targetNames(b.targets))
	}

	// Now we know which targets will be run in this bucket, but they are scrambled. Put them back.
	targetNameMap := make(map[string]struct{}, len(targets))
	for _, t := range targets {
		targetNameMap[t.name] = struct{}{}
	}
	// Iterate through the ordered list of all targets and filter by existence in the bucket.
	targets = targets[:0]
	for _, t := range all {
		if _, ok := targetNameMap[t.name]; ok {
			targets = append(targets, t)
		}
	}
	return targets
}

// targetNames returns each target's name in a slice. Useful for debug/print purposes.
func targetNames(targets []target) []string {
	var targetNames []string
	for _, t := range targets {
		targetNames = append(targetNames, t.name)
	}
	return targetNames
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
