// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/microsoft/go-infra/cmd/releaseagent/internal/azdopipeline"
	"github.com/microsoft/go-infra/cmd/releaseagent/internal/goimagesworkflow"
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

func TestGoImagesModeFromBuild(t *testing.T) {
	tests := []struct {
		name  string
		build *azdopipeline.Build
		want  goimagesworkflow.Mode
	}{
		{name: "correlated test", build: &azdopipeline.Build{
			Parameters: map[string]string{"ReleaseUIGoImagesMode": "test"},
		}, want: goimagesworkflow.ModeTest},
		{name: "dev prefix", build: &azdopipeline.Build{
			TemplateParameters: map[string]any{"publishRepoPrefix": "dev/"},
		}, want: goimagesworkflow.ModeTest},
		{name: "old artifacts", build: &azdopipeline.Build{
			TemplateParameters: map[string]any{"sourceBuildPipelineRunId": "3019035"},
		}, want: goimagesworkflow.ModeRollback},
		{name: "current build", build: &azdopipeline.Build{
			TemplateParameters: map[string]any{"sourceBuildPipelineRunId": "$(Build.BuildId)"},
		}, want: goimagesworkflow.ModeNormal},
		{name: "defaults omitted", build: &azdopipeline.Build{}, want: goimagesworkflow.ModeNormal},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := goImagesModeFromBuild(test.build); got != test.want {
				t.Fatalf("mode = %q, want %q", got, test.want)
			}
		})
	}
}
