// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"go.yaml.in/yaml/v4"
)

type releaseReportPipelineStep struct {
	Template   string
	Script     string
	Parameters map[string]yaml.Node
}

func TestReportCompletionPipelineWiring(t *testing.T) {
	t.Run("report-flag", func(t *testing.T) {
		var template struct {
			Parameters []struct {
				Name, Type string
				Default    yaml.Node
			}
			Steps []map[string][]releaseReportPipelineStep
		}
		readReleaseReportPipelineYAML(t, filepath.Join("steps", "report.yml"), &template)
		found := false
		for _, parameter := range template.Parameters {
			if parameter.Name == "completeRelease" {
				found = true
				if parameter.Type != "boolean" || parameter.Default.Tag != "!!bool" || parameter.Default.Value != "false" {
					t.Errorf("completion parameter must default to false: %+v", parameter)
				}
			}
		}
		if !found {
			t.Error("report template has no completion parameter")
		}
		flags := 0
		for _, conditional := range template.Steps {
			for _, steps := range conditional {
				for _, step := range steps {
					if strings.Contains(step.Script, "-complete-release=${{ parameters.completeRelease }}") {
						flags++
					}
				}
			}
		}
		if flags != 1 {
			t.Errorf("found %d report completion flags, want 1", flags)
		}
	})

	t.Run("final-report-only", func(t *testing.T) {
		var template struct {
			Parameters map[string]yaml.Node
			Jobs       []struct {
				Steps []releaseReportPipelineStep
			}
		}
		readReleaseReportPipelineYAML(t, filepath.Join("jobs", "releasego.yml"), &template)
		parameter := template.Parameters["completeRelease"]
		if parameter.Tag != "!!bool" || parameter.Value != "false" {
			t.Errorf("job completion must default to false: %+v", parameter)
		}
		if len(template.Jobs) != 1 || len(template.Jobs[0].Steps) == 0 {
			t.Fatal("expected one release job with steps")
		}
		steps := template.Jobs[0].Steps
		final := steps[len(steps)-1]
		if final.Template != "../steps/report.yml" || final.Parameters["completeRelease"].Value != "${{ parameters.completeRelease }}" {
			t.Errorf("final report does not forward the completion parameter: %+v", final)
		}
		for _, step := range steps[:len(steps)-1] {
			if _, found := step.Parameters["completeRelease"]; found {
				t.Error("an earlier step forwards the completion parameter")
			}
		}
	})

	t.Run("images-opt-in", func(t *testing.T) {
		var pipeline struct {
			Extends struct {
				Parameters struct {
					Stages []struct {
						Jobs []releaseReportPipelineStep
					}
				}
			}
		}
		readReleaseReportPipelineYAML(t, "release-go-images-pipeline.yml", &pipeline)
		jobs := 0
		for _, stage := range pipeline.Extends.Parameters.Stages {
			for _, job := range stage.Jobs {
				if job.Template == "/eng/pipelines/jobs/releasego.yml@self" {
					jobs++
					parameter := job.Parameters["completeRelease"]
					if parameter.Tag != "!!bool" || parameter.Value != "true" {
						t.Error("image-release job does not explicitly enable completion")
					}
				}
			}
		}
		if jobs != 1 {
			t.Errorf("found %d image-release jobs, want 1", jobs)
		}
	})
}

func readReleaseReportPipelineYAML(t *testing.T, name string, value any) {
	t.Helper()
	path := filepath.Join("..", "..", "eng", "pipelines", name)
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := yaml.Unmarshal(data, value); err != nil {
		t.Fatalf("parse %s: %v", path, err)
	}
}
