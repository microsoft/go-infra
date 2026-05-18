// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package telemetry

import "testing"

func TestDetectCI(t *testing.T) {
	tests := []struct {
		name string
		env  []string
		want string
	}{
		{"no CI", nil, ""},
		{"Azure Pipelines", []string{"TF_BUILD=True"}, "azdo"},
		{"GitHub Actions", []string{"GITHUB_ACTIONS=true"}, "github"},
		{"GitLab CI", []string{"GITLAB_CI=true"}, "gitlab"},
		{"AppVeyor", []string{"APPVEYOR=True"}, "appveyor"},
		{"Travis CI", []string{"TRAVIS=true"}, "travis"},
		{"CircleCI", []string{"CIRCLECI=true"}, "circleci"},
		{"AWS CodeBuild", []string{"CODEBUILD_BUILD_ID=build:123", "AWS_REGION=us-east-1"}, "aws_codebuild"},
		{"Jenkins", []string{"BUILD_ID=42", "BUILD_URL=http://jenkins/job/42"}, "jenkins"},
		{"Google Cloud Build", []string{"BUILD_ID=abc", "PROJECT_ID=my-project"}, "google_cloud_build"},
		{"TeamCity", []string{"TEAMCITY_VERSION=2023.05"}, "teamcity"},
		{"TF_BUILD false not detected", []string{"TF_BUILD=false"}, ""},
		{"GITHUB_ACTIONS empty not detected", []string{"GITHUB_ACTIONS="}, ""},
		{"AWS CodeBuild missing region", []string{"CODEBUILD_BUILD_ID=build:123"}, ""},
		{"Jenkins missing BUILD_URL", []string{"BUILD_ID=42"}, ""},
		{"azdo wins over generic CI", []string{"TF_BUILD=True", "CI=true"}, "azdo"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := DetectCI(tt.env); got != tt.want {
				t.Errorf("DetectCI() = %q, want %q", got, tt.want)
			}
		})
	}
}
