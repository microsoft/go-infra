// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package main

import (
	"strings"
	"testing"

	"github.com/microsoft/go-infra/cmd/releaseagent/internal/azdopipeline"
	"github.com/microsoft/go-infra/cmd/releaseagent/internal/releasesteps"
)

func TestValidateServeOptions(t *testing.T) {
	for _, test := range []struct {
		name          string
		sessionFile   string
		goInfraGitHub bool
		wantError     string
	}{
		{name: "default"},
		{name: "go-infra with journal", sessionFile: "session.json", goInfraGitHub: true},
		{name: "go-infra without journal", goInfraGitHub: true, wantError: "requires -session-file"},
	} {
		t.Run(test.name, func(t *testing.T) {
			err := validateServeOptions(test.sessionFile, test.goInfraGitHub)
			if test.wantError == "" && err != nil {
				t.Fatal(err)
			}
			if test.wantError != "" && (err == nil || !strings.Contains(err.Error(), test.wantError)) {
				t.Fatalf("error = %v, want substring %q", err, test.wantError)
			}
		})
	}
}

func TestGoImagesModeFromBuild(t *testing.T) {
	tests := []struct {
		name  string
		build *azdopipeline.Build
		want  releasesteps.GoImagesReleaseMode
	}{
		{name: "correlated test", build: &azdopipeline.Build{
			Parameters: map[string]string{"ReleaseUIGoImagesMode": "test"},
		}, want: releasesteps.GoImagesReleaseModeTest},
		{name: "dev prefix", build: &azdopipeline.Build{
			TemplateParameters: map[string]any{"publishRepoPrefix": "dev/"},
		}, want: releasesteps.GoImagesReleaseModeTest},
		{name: "old artifacts", build: &azdopipeline.Build{
			TemplateParameters: map[string]any{"sourceBuildPipelineRunId": "3019035"},
		}, want: releasesteps.GoImagesReleaseModeRollback},
		{name: "current build", build: &azdopipeline.Build{
			TemplateParameters: map[string]any{"sourceBuildPipelineRunId": "$(Build.BuildId)"},
		}, want: releasesteps.GoImagesReleaseModeNormal},
		{name: "defaults omitted", build: &azdopipeline.Build{}, want: releasesteps.GoImagesReleaseModeNormal},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := goImagesModeFromBuild(test.build); got != test.want {
				t.Fatalf("mode = %q, want %q", got, test.want)
			}
		})
	}
}
