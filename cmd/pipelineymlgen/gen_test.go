// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package main

import (
	"os"
	"os/exec"
	"strings"
	"testing"
)

func TestGoInfraPipelineGenReproducible(t *testing.T) {
	cmd := exec.Command("go", "run", "github.com/microsoft/go-infra/cmd/pipelineymlgen", "-exit-code", "-r", "../../eng/pipelines")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("pipelineymlgen reproduciblility check failed: %v\nOutput:\n%s\nRun 'go generate ./cmd/pipelineymlgen' if differences are expected.", err, out)
	}
}

func TestGoImagesReleaseWaitsForProvidedBuildID(t *testing.T) {
	content, err := os.ReadFile("../../eng/pipelines/release-go-images-pipeline.yml")
	if err != nil {
		t.Fatal(err)
	}

	var pollBuildIDConditionIndent, waitBuildScriptIndent int
	lines := strings.Split(string(content), "\n")
	for i, line := range lines {
		if strings.Contains(line, "if eq(parameters.poll2MicrosoftGoImagesBuildID, 'nil')") {
			pollBuildIDConditionIndent = leadingSpaces(line)
		}
		if strings.Contains(line, "releasego wait-build") {
			for j := i - 1; j >= 0; j-- {
				if strings.Contains(lines[j], "- script: |") {
					waitBuildScriptIndent = leadingSpaces(lines[j])
					break
				}
			}
			break
		}
	}
	if pollBuildIDConditionIndent == 0 {
		t.Fatal("didn't find poll2MicrosoftGoImagesBuildID condition")
	}
	if waitBuildScriptIndent == 0 {
		t.Fatal("didn't find go-images wait-build script")
	}
	if waitBuildScriptIndent != pollBuildIDConditionIndent {
		t.Fatalf("go-images wait-build script is nested under the provided-build-ID skip condition: script indent = %d, condition indent = %d", waitBuildScriptIndent, pollBuildIDConditionIndent)
	}
}

func leadingSpaces(s string) int {
	return len(s) - len(strings.TrimLeft(s, " "))
}
