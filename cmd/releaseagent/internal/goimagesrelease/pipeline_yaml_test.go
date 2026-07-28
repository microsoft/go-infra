// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package goimagesrelease

import (
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"

	"go.yaml.in/yaml/v4"
)

func TestReleaseUIIntegrationPipelineIsSafeNoOp(t *testing.T) {
	_, sourceFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("unable to locate test source file")
	}
	pipelinePath := filepath.Join(
		filepath.Dir(sourceFile),
		"..", "..", "..", "..",
		"eng", "pipelines", "release-ui-integration-test-pipeline.yml",
	)
	data, err := os.ReadFile(pipelinePath)
	if err != nil {
		t.Fatal(err)
	}
	var pipeline map[string]any
	if err := yaml.Unmarshal(data, &pipeline); err != nil {
		t.Fatalf("parse pipeline YAML: %v", err)
	}
	if pipeline["trigger"] != "none" || pipeline["pr"] != "none" {
		t.Fatalf("pipeline must be manual-only: trigger=%#v pr=%#v", pipeline["trigger"], pipeline["pr"])
	}
	if _, ok := pipeline["variables"]; ok {
		t.Fatal("test pipeline must not import variables or variable groups")
	}

	parameters, ok := pipeline["parameters"].([]any)
	if !ok {
		t.Fatalf("parameters have unexpected type %T", pipeline["parameters"])
	}
	var names []string
	for _, value := range parameters {
		parameter, ok := value.(map[string]any)
		if !ok {
			t.Fatalf("parameter has unexpected type %T", value)
		}
		name, _ := parameter["name"].(string)
		names = append(names, name)
	}
	wantNames := []string{
		"releaseVersions",
		"releaseIssue",
		"isSecurityRelease",
		"approveAheadOfTime",
		"runGoImagesBuild",
		"runPublishAnnouncement",
		"runUpdateDL",
		"runGoImageVersionCheck",
		"poll1MicrosoftGoImagesCommitHash",
		"poll2MicrosoftGoImagesBuildID",
		"notify",
		"goReleaseConfigVariableGroup",
	}
	if !reflect.DeepEqual(names, wantNames) {
		t.Fatalf("parameter names = %#v, want %#v", names, wantNames)
	}

	text := string(data)
	for _, forbidden := range []string{
		"group:",
		"PublishBuildArtifacts",
		"PublishPipelineArtifact",
		"AzureCLI@",
		"Docker@",
		"GitHub",
		"checkout: self",
	} {
		if strings.Contains(text, forbidden) {
			t.Errorf("pipeline unexpectedly contains %q", forbidden)
		}
	}
	if !strings.Contains(text, "checkout: none") {
		t.Error("pipeline must explicitly disable source checkout")
	}
	if !strings.Contains(text, "$(ReleaseUISessionID)") {
		t.Error("pipeline must validate the release UI correlation variable")
	}
}
