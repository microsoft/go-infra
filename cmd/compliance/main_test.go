// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package main

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestParsePortalURL(t *testing.T) {
	const portalURL = "https://dev.azure.com/example-org/ExampleProject/_compliance/product/00000000-0000-0000-0000-000000000000/assessments"
	target, err := parsePortalURL(portalURL)
	if err != nil {
		t.Fatal(err)
	}
	if target.Organization != "example-org" || target.Project != "ExampleProject" || target.ProductID != "00000000-0000-0000-0000-000000000000" {
		t.Fatalf("target = %+v", target)
	}
}

func TestParsePortalURLRejectsOtherHostsAndRoutes(t *testing.T) {
	for _, value := range []string{
		"https://example.com/example-org/ExampleProject/_compliance/product/id/assessments",
		"https://dev.azure.com/example-org/ExampleProject/_compliance/product/id",
		"http://dev.azure.com/example-org/ExampleProject/_compliance/product/id/assessments",
	} {
		t.Run(value, func(t *testing.T) {
			if _, err := parsePortalURL(value); err == nil {
				t.Fatal("parsePortalURL succeeded, want error")
			}
		})
	}
}

func TestAcquireAzureDevOpsTokenUsesPATEnvironment(t *testing.T) {
	t.Setenv("AZURE_DEVOPS_EXT_PAT", "test-pat")
	token, err := acquireAzureDevOpsToken(context.Background(), "does-not-exist")
	if err != nil {
		t.Fatal(err)
	}
	if token != "test-pat" {
		t.Fatal("token does not match environment value")
	}
}

func TestRunCLIRequiresPinnedGroupWhenApplying(t *testing.T) {
	const portalURL = "https://dev.azure.com/example-org/ExampleProject/_compliance/product/00000000-0000-0000-0000-000000000000/assessments"
	var stdout, stderr bytes.Buffer
	err := runCLI(context.Background(), []string{"-url", portalURL, "-apply"}, &stdout, &stderr)
	if err == nil || !strings.Contains(err.Error(), "-assessment-group is required") {
		t.Fatalf("error = %v", err)
	}
}

func TestLoadAnswerOverrides(t *testing.T) {
	path := filepath.Join(t.TempDir(), "answers.json")
	if err := os.WriteFile(path, []byte(`{"Changed Questionnaire":[{"questionId":"question","answers":["Yes"]}]}`), 0o600); err != nil {
		t.Fatal(err)
	}

	overrides, err := loadAnswerOverrides(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := len(overrides["Changed Questionnaire"]); got != 1 {
		t.Fatalf("answer count = %d, want 1", got)
	}
}

func TestParseCompleteActivities(t *testing.T) {
	activities, err := parseCompleteActivities([]string{"Changed Questionnaire=policy.activity.new"})
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := activities["Changed Questionnaire"]["policy.activity.new"]; !ok {
		t.Fatal("activity was not parsed")
	}
	if _, err := parseCompleteActivities([]string{"missing-separator"}); err == nil {
		t.Fatal("invalid activity succeeded")
	}
}
