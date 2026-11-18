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
	const env = "GO_INFRA_EXECUTIL_OUTPUT"
	if os.Getenv(env) != "" {
		fmt.Fprint(os.Stdout, "expected stdout")
		fmt.Fprint(os.Stderr, "expected failure output")
		if os.Getenv(env) == "fail" {
			os.Exit(1)
		}
		os.Exit(0)
	}

	cmd := exec.Command(os.Args[0], "-test.run=^TestCombinedOutputReturnsOutputOnError$")
	cmd.Env = append(os.Environ(), env+"=fail")
	output, err := CombinedOutput(cmd)
	if err == nil {
		t.Fatal("CombinedOutput succeeded, want error")
	}
	if !strings.Contains(output, "expected failure output") {
		t.Errorf("CombinedOutput output = %q, want failure output", output)
	}
}

func TestOutputSeparatesStderr(t *testing.T) {
	const env = "GO_INFRA_EXECUTIL_OUTPUT"
	for _, fail := range []bool{false, true} {
		t.Run(fmt.Sprintf("fail=%v", fail), func(t *testing.T) {
			value := "succeed"
			if fail {
				value = "fail"
			}
			cmd := exec.Command(os.Args[0], "-test.run=^TestCombinedOutputReturnsOutputOnError$")
			cmd.Env = append(os.Environ(), env+"="+value)
			output, err := Output(cmd)
			if output != "expected stdout" {
				t.Errorf("Output output = %q, want stdout only", output)
			}
			if fail {
				if err == nil || !strings.Contains(err.Error(), "expected failure output") {
					t.Errorf("Output error = %v, want stderr diagnostics", err)
				}
			} else if err != nil {
				t.Fatal(err)
			}
		})
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
