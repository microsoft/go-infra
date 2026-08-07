// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package main

import (
	"testing"

	"github.com/microsoft/go-infra/cmd/releaseagent/internal/azdopipeline"
	"github.com/microsoft/go-infra/cmd/releaseagent/internal/releasesteps"
)

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
