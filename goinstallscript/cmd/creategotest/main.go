// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package main

import (
	_ "embed"
	"fmt"
	"os"
	"path/filepath"
)

//go:embed _template/goinstallscript_test.go
var goTestTemplate string

const goTestFilePath = "goinstallscript/goinstallscript_test.go"

func main() {
	if err := run(); err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
}

func run() error {
	if err := os.MkdirAll(filepath.Dir(goTestFilePath), 0o755); err != nil {
		return err
	}
	if err := os.WriteFile(goTestFilePath, []byte(goTestTemplate), 0o644); err != nil {
		return err
	}
	fmt.Println("Created " + goTestFilePath)
	return nil
}
