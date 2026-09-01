// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package executil

import (
	"fmt"
	"os"
	"os/exec"
	"path"
	"strings"
	"testing"
)

func TestCombinedOutputReturnsOutputOnError(t *testing.T) {
	const env = "GO_INFRA_EXECUTIL_FAILURE"
	if os.Getenv(env) == "1" {
		fmt.Fprint(os.Stderr, "expected failure output")
		os.Exit(1)
	}

	cmd := exec.Command(os.Args[0], "-test.run=^TestCombinedOutputReturnsOutputOnError$")
	cmd.Env = append(os.Environ(), env+"=1")
	output, err := CombinedOutput(cmd)
	if err == nil {
		t.Fatal("CombinedOutput succeeded, want error")
	}
	if !strings.Contains(output, "expected failure output") {
		t.Errorf("CombinedOutput output = %q, want failure output", output)
	}
}

func TestMakeWorkDir(t *testing.T) {
	tests := []struct {
		name    string
		rootDir string
	}{
		{"InsideExistingDir", t.TempDir()},
		{"InsideNonexistentDir", path.Join(t.TempDir(), "nonexistent")},
		{"DeeplyInsideNonexistentDir", path.Join(t.TempDir(), "nonexistent", "a", "b", "c")},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := MakeWorkDir(tt.rootDir)
			if err != nil {
				t.Error(err)
			}
		})
	}
}
