// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package goimagesrelease

import (
	"context"
	"reflect"
	"testing"

	"github.com/microsoft/go-infra/cmd/releaseagent/internal/azdopipeline"
	"github.com/microsoft/go-infra/cmd/releaseagent/internal/goimagesworkflow"
)

const testCommit = "81ce9afc2b75ec4e153dd15fc3c7539b12024945"

type fakePipelineClient struct {
	build *azdopipeline.Build
}

func (c *fakePipelineClient) Get(context.Context, int) (*azdopipeline.Build, error) {
	return c.build, nil
}

func TestValidateRollbackSource(t *testing.T) {
	client := &fakePipelineClient{build: &azdopipeline.Build{
		ID: 3019035, DefinitionID: goimagesworkflow.DefinitionID, Status: "completed", Result: "succeeded",
		WebURL:       "https://example/build/3019035",
		SourceBranch: goimagesworkflow.SourceBranch, SourceVersion: testCommit,
		TemplateParameters: map[string]any{
			"sourceBuildPipelineRunId": "$(Build.BuildId)",
			"publishRepoPrefix":        "public/",
		},
	}}
	source, err := ValidateRollbackSource(
		context.Background(),
		client,
		VersionResolverFunc(func(context.Context, string) ([]string, error) {
			return []string{"1.26.5-2", "1.25.12-1"}, nil
		}),
		3019035,
	)
	if err != nil {
		t.Fatal(err)
	}
	if source.BuildID != 3019035 || source.URL != "https://example/build/3019035" ||
		!reflect.DeepEqual(source.Versions, []string{"1.25.12-1", "1.26.5-2"}) {

		t.Fatalf("source = %#v", source)
	}
}

func TestValidateRollbackSourceRejectsUnsafeBuilds(t *testing.T) {
	for _, test := range []struct {
		name  string
		build *azdopipeline.Build
	}{
		{name: "wrong definition", build: &azdopipeline.Build{
			ID: 1, DefinitionID: 1492, Status: "completed", Result: "succeeded",
			SourceBranch: goimagesworkflow.SourceBranch, SourceVersion: testCommit,
		}},
		{name: "failed", build: &azdopipeline.Build{
			ID: 1, DefinitionID: goimagesworkflow.DefinitionID, Status: "completed", Result: "failed",
			SourceBranch: goimagesworkflow.SourceBranch, SourceVersion: testCommit,
		}},
		{name: "wrong branch", build: &azdopipeline.Build{
			ID: 1, DefinitionID: goimagesworkflow.DefinitionID, Status: "completed", Result: "succeeded",
			SourceBranch: "refs/heads/feature", SourceVersion: testCommit,
		}},
		{name: "already republished", build: &azdopipeline.Build{
			ID: 1, DefinitionID: goimagesworkflow.DefinitionID, Status: "completed", Result: "succeeded",
			SourceBranch: goimagesworkflow.SourceBranch, SourceVersion: testCommit,
			TemplateParameters: map[string]any{"sourceBuildPipelineRunId": "123"},
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			_, err := ValidateRollbackSource(
				context.Background(),
				&fakePipelineClient{build: test.build},
				VersionResolverFunc(func(context.Context, string) ([]string, error) {
					return []string{"1.26.5-2"}, nil
				}),
				test.build.ID,
			)
			if err == nil {
				t.Fatal("unsafe rollback source was accepted")
			}
		})
	}
}

func TestNormalizeVersions(t *testing.T) {
	got, err := normalizeVersions([]string{" 1.26.5-2 ", "1.25.12-1"})
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, []string{"1.25.12-1", "1.26.5-2"}) {
		t.Fatalf("versions = %v", got)
	}
	for _, versions := range [][]string{nil, {""}, {"1.26.5-2", "1.26.5-2"}} {
		if _, err := normalizeVersions(versions); err == nil {
			t.Fatalf("invalid versions %q were accepted", versions)
		}
	}
}
