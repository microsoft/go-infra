// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

// Package goldentest is a utility to help create tests that compare a result (e.g. serialized data
// or formatted text) against a golden file stored in testdata.
package goldentest

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"testing"
)

// update is a flag that can be set using "go test . -update", and it is registered here when this
// package is initialized.
//
// Note that "go test ./... -update" emits errors in many cases. Packages with tests that don't
// import goldentest don't have "-update" registered, and they fail noisily when they see the
// unknown flag. For this reason, Check also supports "go test ./... -args update".
//
// Both local and global update commands are included in the error message when a Check fails.
var update = flag.Bool("update", false, "Update the golden files instead of failing.")

// Check looks for a file at testdata/{t.Name()}/[goldenPath], compares [actual] against the
// content, and causes the test to fail if it's incorrect. If "-update" or "-args update" is passed
// to the "go test" command, instead of failing, writes [actual] to the file.
func Check(t *testing.T, goldenPath, actual string) {
	t.Helper()

	if slices.Contains(flag.Args(), "update") {
		*update = true
	}

	path := filepath.Join("testdata", t.Name(), goldenPath)

	if *update {
		if err := os.MkdirAll(filepath.Dir(path), os.ModePerm); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(actual), 0o666); err != nil {
			t.Fatal(err)
		}
	}

	wd, err := os.Getwd()
	if err != nil {
		t.Fatalf("failed to get working directory: %v", err)
	}

	runHelp := fmt.Sprintf(
		"To regenerate all golden files, run in the module root: "+
			"go test ./... -args update\n"+
			"To regenerate just this test's golden file, run: "+
			"go test '%v' -run '^%v$' -update",
		wd, t.Name())

	want, err := os.ReadFile(path)
	if err != nil {
		t.Errorf("Unable to read golden file: %v.\n%v", err, runHelp)
	} else if actual != string(want) {
		t.Errorf("Actual result didn't match golden file. Regenerate the golden file and examine the Git diff to determine if the change is acceptable.\n%v", runHelp)
	}
}
