// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package main

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"
)

func TestRunCLIConfirmed(t *testing.T) {
	target := repository{Owner: "microsoft", Name: "go-infra"}
	var got elevationRequest
	var calls int
	deps := cliDependencies{
		getwd: func() (string, error) {
			return "/work", nil
		},
		detect: func(ctx context.Context, directory string) (repository, error) {
			if directory != "/work" {
				t.Fatalf("directory = %q, want /work", directory)
			}
			return target, nil
		},
		elevate: func(ctx context.Context, request elevationRequest, options browserOptions) error {
			calls++
			got = request
			return nil
		},
	}
	var stdout, stderr bytes.Buffer
	err := runCLI(
		context.Background(),
		nil,
		strings.NewReader("Investigate release pipeline failure\nyes\n"),
		&stdout,
		&stderr,
		deps,
	)
	if err != nil {
		t.Fatal(err)
	}
	if calls != 1 {
		t.Fatalf("elevation calls = %d, want 1", calls)
	}
	if got.Repository != target {
		t.Errorf("repository = %v, want %v", got.Repository, target)
	}
	if got.Description != "Investigate release pipeline failure" {
		t.Errorf("description = %q", got.Description)
	}
	if !strings.Contains(stdout.String(), "JIT administrator elevation granted") {
		t.Errorf("stdout did not report success:\n%s", stdout.String())
	}
}

func TestRunCLICancelled(t *testing.T) {
	deps := cliDependencies{
		getwd: func() (string, error) {
			return "/work", nil
		},
		detect: func(context.Context, string) (repository, error) {
			return repository{Owner: "microsoft", Name: "go-infra"}, nil
		},
		elevate: func(context.Context, elevationRequest, browserOptions) error {
			t.Fatal("elevate was called after cancellation")
			return nil
		},
	}
	var stdout, stderr bytes.Buffer
	err := runCLI(
		context.Background(),
		nil,
		strings.NewReader("Routine maintenance\nno\n"),
		&stdout,
		&stderr,
		deps,
	)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(stdout.String(), "Cancelled") {
		t.Errorf("stdout did not report cancellation:\n%s", stdout.String())
	}
}

func TestRunCLIRepositoryOverride(t *testing.T) {
	var got repository
	deps := cliDependencies{
		getwd: func() (string, error) {
			t.Fatal("getwd was called with -repo")
			return "", nil
		},
		detect: func(context.Context, string) (repository, error) {
			t.Fatal("detect was called with -repo")
			return repository{}, nil
		},
		elevate: func(ctx context.Context, request elevationRequest, options browserOptions) error {
			got = request.Repository
			return nil
		},
	}
	var stdout, stderr bytes.Buffer
	err := runCLI(
		context.Background(),
		[]string{"-repo", "Azure/azure-sdk-for-go"},
		strings.NewReader("Debug package publishing\ny\n"),
		&stdout,
		&stderr,
		deps,
	)
	if err != nil {
		t.Fatal(err)
	}
	want := repository{Owner: "Azure", Name: "azure-sdk-for-go"}
	if got != want {
		t.Errorf("repository = %v, want %v", got, want)
	}
}

func TestRunCLIRepositoryPositionalArgument(t *testing.T) {
	var got repository
	deps := cliDependencies{
		getwd: func() (string, error) {
			t.Fatal("getwd was called with a positional repository")
			return "", nil
		},
		detect: func(context.Context, string) (repository, error) {
			t.Fatal("detect was called with a positional repository")
			return repository{}, nil
		},
		elevate: func(ctx context.Context, request elevationRequest, options browserOptions) error {
			got = request.Repository
			return nil
		},
	}
	var stdout, stderr bytes.Buffer
	err := runCLI(
		context.Background(),
		[]string{"Azure/azure-sdk-for-go"},
		strings.NewReader("Debug package publishing\ny\n"),
		&stdout,
		&stderr,
		deps,
	)
	if err != nil {
		t.Fatal(err)
	}
	want := repository{Owner: "Azure", Name: "azure-sdk-for-go"}
	if got != want {
		t.Errorf("repository = %v, want %v", got, want)
	}
}

func TestRunCLIRequiresDescription(t *testing.T) {
	deps := cliDependencies{
		getwd: func() (string, error) {
			return "/work", nil
		},
		detect: func(context.Context, string) (repository, error) {
			return repository{Owner: "microsoft", Name: "go-infra"}, nil
		},
		elevate: func(context.Context, elevationRequest, browserOptions) error {
			t.Fatal("elevate was called without a description")
			return nil
		},
	}
	err := runCLI(
		context.Background(),
		nil,
		strings.NewReader(" \n"),
		&bytes.Buffer{},
		&bytes.Buffer{},
		deps,
	)
	if err == nil || !strings.Contains(err.Error(), "description must not be empty") {
		t.Fatalf("error = %v", err)
	}
}

func TestRunCLIPropagatesElevationError(t *testing.T) {
	deps := cliDependencies{
		getwd: func() (string, error) {
			return "/work", nil
		},
		detect: func(context.Context, string) (repository, error) {
			return repository{Owner: "microsoft", Name: "go-infra"}, nil
		},
		elevate: func(context.Context, elevationRequest, browserOptions) error {
			return errors.New("portal unavailable")
		},
	}
	err := runCLI(
		context.Background(),
		nil,
		strings.NewReader("Routine maintenance\ny\n"),
		&bytes.Buffer{},
		&bytes.Buffer{},
		deps,
	)
	if err == nil || !strings.Contains(err.Error(), "portal unavailable") {
		t.Fatalf("error = %v", err)
	}
}

func TestParseFlagsRejectsNonPositiveTimeout(t *testing.T) {
	_, err := parseFlags([]string{"-timeout", "0s"}, &bytes.Buffer{})
	if err == nil || !strings.Contains(err.Error(), "greater than zero") {
		t.Fatalf("error = %v", err)
	}
}

func TestParseFlagsRejectsDuplicateRepository(t *testing.T) {
	_, err := parseFlags([]string{"-repo", "microsoft/go", "microsoft/go-infra"}, &bytes.Buffer{})
	if err == nil || !strings.Contains(err.Error(), "not both") {
		t.Fatalf("error = %v", err)
	}
}

func TestIsConfirmation(t *testing.T) {
	for _, value := range []string{"y", "Y", "yes", " YES "} {
		if !isConfirmation(value) {
			t.Errorf("isConfirmation(%q) = false", value)
		}
	}
	for _, value := range []string{"", "n", "no", "true", "1"} {
		if isConfirmation(value) {
			t.Errorf("isConfirmation(%q) = true", value)
		}
	}
}
