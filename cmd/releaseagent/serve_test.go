// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestProcessRunStorePathRejectsLegacyJournal(t *testing.T) {
	sessionPath := filepath.Join(t.TempDir(), "session.json")
	path, err := processRunStorePath(sessionPath)
	if err != nil {
		t.Fatal(err)
	}
	if want := sessionPath + ".process-run.json"; path != want {
		t.Fatalf("path = %q, want %q", path, want)
	}
	legacyPath := sessionPath + ".go-infra-action.json"
	if err := os.WriteFile(legacyPath, []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := processRunStorePath(sessionPath); err == nil || !strings.Contains(err.Error(), "legacy go-infra action journal") {
		t.Fatalf("error = %v", err)
	}
}
