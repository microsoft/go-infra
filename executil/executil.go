// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

// Package executil contains some common wrappers for simple use of exec.Cmd.
package executil

import (
	"fmt"
	"os"
	"os/exec"
	"strings"
)

// Run sets up the command to log directly to our stdout/stderr streams, then runs it.
func Run(c *exec.Cmd) error {
	c.Stdout = os.Stdout
	c.Stderr = os.Stderr
	return RunQuiet(c)
}

// RunQuiet logs the command line and runs the given command, but sends the output to os.DevNull.
func RunQuiet(c *exec.Cmd) error {
	fmt.Printf("---- Running command: %v %v\n", c.Path, c.Args)
	return c.Run()
}

// CombinedOutput runs a command and returns the output string of c.CombinedOutput.
func CombinedOutput(c *exec.Cmd) (string, error) {
	fmt.Printf("---- Running command: %v %v\n", c.Path, c.Args)
	out, err := c.CombinedOutput()
	if err != nil {
		return "", err
	}
	return string(out), nil
}

// SpaceTrimmedCombinedOutput runs CombinedOutput and trims leading/trailing spaces from the result.
func SpaceTrimmedCombinedOutput(c *exec.Cmd) (string, error) {
	out, err := CombinedOutput(c)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(out), nil
}
