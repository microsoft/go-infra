// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package azdopipeline

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"
)

type fakeCommandRunner struct {
	name   string
	args   []string
	output []byte
	err    error
	calls  int
}

func (r *fakeCommandRunner) Output(_ context.Context, name string, args ...string) ([]byte, error) {
	r.calls++
	r.name = name
	r.args = append([]string(nil), args...)
	return r.output, r.err
}

func TestAzureCLITokenProvider(t *testing.T) {
	runner := &fakeCommandRunner{output: []byte("test-token\n")}
	token, err := (AzureCLITokenProvider{Runner: runner}).Token(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if token != "test-token" || runner.name != "az" {
		t.Fatalf("token = %q, command = %q", token, runner.name)
	}
	wantArgs := []string{
		"account", "get-access-token",
		"--resource", AzureDevOpsResourceID,
		"--query", "accessToken",
		"--output", "tsv",
	}
	if !reflect.DeepEqual(runner.args, wantArgs) {
		t.Fatalf("args = %#v, want %#v", runner.args, wantArgs)
	}
}

func TestAzureCLITokenProviderDoesNotExposeOutput(t *testing.T) {
	runner := &fakeCommandRunner{
		output: []byte("secret-token"),
		err:    errors.New("exit status 1"),
	}
	_, err := (AzureCLITokenProvider{Runner: runner}).Token(context.Background())
	if err == nil {
		t.Fatal("expected error")
	}
	if strings.Contains(err.Error(), "secret-token") {
		t.Fatalf("error exposed command output: %v", err)
	}
}

func TestCachingTokenProvider(t *testing.T) {
	runner := &fakeCommandRunner{output: []byte("test-token\n")}
	provider := &CachingTokenProvider{
		Provider: AzureCLITokenProvider{Runner: runner},
		TTL:      time.Minute,
	}
	for range 2 {
		token, err := provider.Token(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		if token != "test-token" {
			t.Fatalf("token = %q", token)
		}
	}
	if runner.calls != 1 {
		t.Fatalf("Azure CLI calls = %d, want 1", runner.calls)
	}
}
