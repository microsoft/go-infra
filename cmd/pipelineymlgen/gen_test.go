// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package main

import (
	"os/exec"
	"testing"
)

func TestGoInfraPipelineGenReproducible(t *testing.T) {
	cmd := exec.Command("go", "run", "github.com/microsoft/go-infra/cmd/pipelineymlgen", "-exit-code", "-r", "../../eng/pipelines")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("pipelineymlgen reproduciblility check failed: %v\nOutput:\n%s\nRun 'go generate ./cmd/pipelineymlgen' if differences are expected.", err, out)
	}
}
