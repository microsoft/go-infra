// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

// Package goldentest is a utility to help create tests that compare a result (e.g. serialized data
// or formatted text) against a golden file stored in testdata.
package goldentest

import (
	"flag"
	"os"
	"path/filepath"
	"testing"
)

// update is a flag that can be set using "go test . -update", and it is registered here when this
// package is initialized.
var update = flag.Bool("update", false, "Update the golden files instead of failing.")

// Check looks for a file at goldenPath, compares actual against the content, and causes the test to
// fail if it's incorrect. If "-update" is passed to the "go test" command, instead of failing, it
// writes actual to goldenPath.
func Check(t *testing.T, rerunCmd, goldenPath, actual string) {
	t.Helper()

	if *update {
		if err := os.MkdirAll(filepath.Dir(goldenPath), os.ModePerm); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(goldenPath, []byte(actual), 0o666); err != nil {
			t.Fatal(err)
		}
	}

	runHelp := "Run '" + rerunCmd + " -update' to update golden file"

	want, err := os.ReadFile(goldenPath)
	if err != nil {
		t.Fatalf("Unable to read golden file. %v. Error: %v", runHelp, err)
	}

	if actual != string(want) {
		t.Errorf("Actual result didn't match golden file. %v and examine the Git diff to determine if the change is acceptable.", runHelp)
	}
}
