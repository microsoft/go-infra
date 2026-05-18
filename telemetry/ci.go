// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package telemetry

import "strings"

// DetectCI inspects the given environment variables to determine which CI
// systems are in use. The env parameter should be in the same format as
// os.Environ() (i.e. each entry is "KEY=VALUE"). It returns "ci" plus short
// identifiers matching the go/ci counter values, or nil if no CI system is
// detected.
func DetectCI(env []string) []string {
	m := envMap(env)
	var detected []string
	addDetected := func(ci string) {
		if len(detected) == 0 {
			detected = append(detected, "ci")
		}
		detected = append(detected, ci)
	}

	// Azure Pipelines
	// https://docs.microsoft.com/en-us/azure/devops/pipelines/build/variables#system-variables-devops-services
	if isTrue(m["TF_BUILD"]) {
		addDetected("azdo")
	}

	// GitHub Actions
	// https://docs.github.com/en/actions/learn-github-actions/environment-variables#default-environment-variables
	if isTrue(m["GITHUB_ACTIONS"]) {
		addDetected("github")
	}

	// GitLab CI
	// https://docs.gitlab.com/ee/ci/variables/predefined_variables.html
	if m["GITLAB_CI"] != "" {
		addDetected("gitlab")
	}

	// AppVeyor
	// https://www.appveyor.com/docs/environment-variables/
	if isTrue(m["APPVEYOR"]) {
		addDetected("appveyor")
	}

	// Travis CI
	// https://docs.travis-ci.com/user/environment-variables/#default-environment-variables
	if isTrue(m["TRAVIS"]) {
		addDetected("travis")
	}

	// CircleCI
	// https://circleci.com/docs/2.0/env-vars/#built-in-environment-variables
	if isTrue(m["CIRCLECI"]) {
		addDetected("circleci")
	}

	// AWS CodeBuild
	// https://docs.aws.amazon.com/codebuild/latest/userguide/build-env-ref-env-vars.html
	if m["CODEBUILD_BUILD_ID"] != "" && m["AWS_REGION"] != "" {
		addDetected("aws_codebuild")
	}

	// Jenkins
	// https://github.com/jenkinsci/jenkins/blob/master/core/src/main/resources/jenkins/model/CoreEnvironmentContributor/buildEnv.groovy
	if m["BUILD_ID"] != "" && m["BUILD_URL"] != "" {
		addDetected("jenkins")
	}

	// Google Cloud Build
	// https://cloud.google.com/build/docs/configuring-builds/substitute-variable-values#using_default_substitutions
	if m["BUILD_ID"] != "" && m["PROJECT_ID"] != "" {
		addDetected("google_cloud_build")
	}

	// TeamCity
	// https://www.jetbrains.com/help/teamcity/predefined-build-parameters.html#Predefined+Server+Build+Parameters
	if m["TEAMCITY_VERSION"] != "" {
		addDetected("teamcity")
	}

	return detected
}

// envMap converts an os.Environ()-style slice into a map for fast lookup.
func envMap(env []string) map[string]string {
	m := make(map[string]string, len(env))
	for _, e := range env {
		if k, v, ok := strings.Cut(e, "="); ok {
			m[k] = v
		}
	}
	return m
}

// isTrue reports whether the value is a common boolean-true string.
// Matches the logic in dotnet/sdk EnvironmentVariableParser.ParseBool.
func isTrue(v string) bool {
	return v == "1" ||
		strings.EqualFold(v, "true") ||
		strings.EqualFold(v, "yes") ||
		strings.EqualFold(v, "on")
}
