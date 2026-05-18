// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package telemetry

import (
	"slices"
	"testing"
)

func TestDetectCI(t *testing.T) {
	tests := []struct {
		name string
		env  []string
		want []string
	}{
		{"no CI", nil, nil},
		{"Azure Pipelines", []string{"TF_BUILD=True"}, []string{"ci", "azdo"}},
		{"GitHub Actions", []string{"GITHUB_ACTIONS=true"}, []string{"ci", "github"}},
		{"GitLab CI", []string{"GITLAB_CI=true"}, []string{"ci", "gitlab"}},
		{"AppVeyor", []string{"APPVEYOR=True"}, []string{"ci", "appveyor"}},
		{"Travis CI", []string{"TRAVIS=true"}, []string{"ci", "travis"}},
		{"CircleCI", []string{"CIRCLECI=true"}, []string{"ci", "circleci"}},
		{"AWS CodeBuild", []string{"CODEBUILD_BUILD_ID=build:123", "AWS_REGION=us-east-1"}, []string{"ci", "aws_codebuild"}},
		{"Jenkins", []string{"BUILD_ID=42", "BUILD_URL=http://jenkins/job/42"}, []string{"ci", "jenkins"}},
		{"Google Cloud Build", []string{"BUILD_ID=abc", "PROJECT_ID=my-project"}, []string{"ci", "google_cloud_build"}},
		{"TeamCity", []string{"TEAMCITY_VERSION=2023.05"}, []string{"ci", "teamcity"}},
		{"TF_BUILD false not detected", []string{"TF_BUILD=false"}, nil},
		{"GITHUB_ACTIONS empty not detected", []string{"GITHUB_ACTIONS="}, nil},
		{"AWS CodeBuild missing region", []string{"CODEBUILD_BUILD_ID=build:123"}, nil},
		{"Jenkins missing BUILD_URL", []string{"BUILD_ID=42"}, nil},
		{"generic CI ignored while azdo detected", []string{"TF_BUILD=True", "CI=true"}, []string{"ci", "azdo"}},
		{"multiple CI systems detected", []string{"TF_BUILD=True", "GITHUB_ACTIONS=true", "TEAMCITY_VERSION=2023.05"}, []string{"ci", "azdo", "github", "teamcity"}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := DetectCI(tt.env); (got == nil) != (tt.want == nil) || !slices.Equal(got, tt.want) {
				t.Errorf("DetectCI() = %#v, want %#v", got, tt.want)
			}
		})
	}
}
