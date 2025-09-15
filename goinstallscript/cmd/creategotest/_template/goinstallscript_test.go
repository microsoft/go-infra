// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package main

import (
	"os/exec"
	"testing"
)

func TestMicrosoftBuildOfGoInstallScriptIsUpToDate(t *testing.T) {
	cmd := exec.Command("go", "run", "github.com/microsoft/go-infra/goinstallscript", "-check")
	cmd.Dir = ".."
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Errorf("Microsoft build of Go Install Script is out of date:\n%v", string(out))
	}
}
