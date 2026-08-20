// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package goimagesrelease

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/microsoft/go-infra/cmd/releaseagent/internal/azdopipeline"
)

const testCommit = "81ce9afc2b75ec4e153dd15fc3c7539b12024945"

type fakePipelineClient struct {
	getBuilds    []*azdopipeline.Build
	recentBuilds []*azdopipeline.Build
	getErr       error
	getCalls     int
}

func (c *fakePipelineClient) Get(_ context.Context, _ int) (*azdopipeline.Build, error) {
	if c.getErr != nil {
		return nil, c.getErr
	}
	index := c.getCalls
	c.getCalls++
	if index >= len(c.getBuilds) {
		index = len(c.getBuilds) - 1
	}
	return c.getBuilds[index], nil
}

func (c *fakePipelineClient) ListRecent(_ context.Context, _ int) ([]*azdopipeline.Build, error) {
	return c.recentBuilds, nil
}

func TestMonitorRun(t *testing.T) {
	client := &fakePipelineClient{getBuilds: []*azdopipeline.Build{
		{ID: 321, Status: "notStarted"},
		{ID: 321, Status: "inProgress"},
		{ID: 321, Status: "completed", Result: "succeeded"},
	}}
	sleeps := 0
	service := newTestServiceWithSleeper(t, client, func(context.Context, time.Duration) error {
		sleeps++
		return nil
	})
	if err := service.MonitorRun(context.Background(), 321); err != nil {
		t.Fatal(err)
	}
	if client.getCalls != 3 || sleeps != 2 {
		t.Fatalf("get calls = %d, sleeps = %d; want 3 and 2", client.getCalls, sleeps)
	}
}

func TestMonitorRunFailure(t *testing.T) {
	client := &fakePipelineClient{getBuilds: []*azdopipeline.Build{{
		ID: 321, Status: "completed", Result: "failed", WebURL: "https://example/build/321",
	}}}
	err := newTestService(t, client).MonitorRun(context.Background(), 321)
	if err == nil || !strings.Contains(err.Error(), "failed") || !strings.Contains(err.Error(), "https://example/build/321") {
		t.Fatalf("error = %v, want failed build and URL", err)
	}
}

func TestMonitorRunHonorsCancellation(t *testing.T) {
	client := &fakePipelineClient{getBuilds: []*azdopipeline.Build{{ID: 321, Status: "inProgress"}}}
	service := newTestServiceWithSleeper(t, client, func(ctx context.Context, _ time.Duration) error { return ctx.Err() })
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := service.MonitorRun(ctx, 321); !errors.Is(err, context.Canceled) {
		t.Fatalf("error = %v, want context cancellation", err)
	}
}

func TestCanonicalVersionSet(t *testing.T) {
	got, err := CanonicalVersionSet([]string{"1.26.5-2", "1.25.12-1"})
	if err != nil {
		t.Fatal(err)
	}
	if got != `["1.25.12-1","1.26.5-2"]` {
		t.Fatalf("version set = %q", got)
	}
}

func TestFindCandidatesBySourceCommitVersions(t *testing.T) {
	queueTime := time.Date(2026, 7, 10, 4, 37, 10, 0, time.UTC)
	const otherCommit = "2ef65db89e42942c24e3d8f0b8a8eb52bc86857a"
	client := &fakePipelineClient{recentBuilds: []*azdopipeline.Build{
		{
			ID: 3019035, DefinitionID: 1023, Status: "completed", Result: "succeeded",
			QueueTime: queueTime, SourceBranch: "refs/heads/microsoft/main", SourceVersion: testCommit,
			TemplateParameters: map[string]any{"sourceBuildPipelineRunId": "$(Build.BuildId)", "publishRepoPrefix": "public/"},
		},
		{ID: 3019034, DefinitionID: 1023, Status: "inProgress", SourceVersion: testCommit},
		{ID: 3017643, DefinitionID: 1023, Status: "completed", Result: "succeeded", SourceVersion: otherCommit},
	}}
	resolverCalls := make(map[string]int)
	service, err := New(client, Config{
		DefinitionID: 1023,
		Versions:     []string{"1.26.5-2"},
		VersionResolver: VersionResolverFunc(func(_ context.Context, commit string) ([]string, error) {
			resolverCalls[commit]++
			if commit == testCommit {
				return []string{"1.25.12-1", "1.26.5-2"}, nil
			}
			return []string{"1.25.12-1", "1.26.5-1"}, nil
		}),
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	candidates, err := service.FindCandidates(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(candidates) != 2 || candidates[0].BuildID != 3019035 || candidates[0].SourceVersion != testCommit {
		t.Fatalf("candidates = %#v", candidates)
	}
	if candidates[0].Parameters["publishRepoPrefix"] != "public/" || candidates[0].QueueTime != queueTime {
		t.Fatalf("candidate = %#v", candidates[0])
	}
	if resolverCalls[testCommit] != 1 {
		t.Fatalf("resolver calls for shared commit = %d, want 1", resolverCalls[testCommit])
	}
}

func TestFindCandidatesPropagatesVersionResolutionError(t *testing.T) {
	client := &fakePipelineClient{recentBuilds: []*azdopipeline.Build{{
		ID: 1, DefinitionID: 1023, Status: "completed", Result: "succeeded", SourceVersion: testCommit,
	}}}
	service, err := New(client, Config{
		DefinitionID: 1023,
		Versions:     []string{"1.26.5-2"},
		VersionResolver: VersionResolverFunc(func(context.Context, string) ([]string, error) {
			return nil, errors.New("versions file unavailable")
		}),
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.FindCandidates(context.Background()); err == nil || !strings.Contains(err.Error(), "versions file unavailable") {
		t.Fatalf("error = %v", err)
	}
}

func TestValidateCandidateChecksDefinitionAndCommit(t *testing.T) {
	client := &fakePipelineClient{getBuilds: []*azdopipeline.Build{{
		ID: 3019035, DefinitionID: 1023, Status: "inProgress",
		SourceBranch: "refs/heads/microsoft/main", SourceVersion: testCommit,
	}}}
	candidate, err := newTestService(t, client).ValidateCandidate(context.Background(), 3019035)
	if err != nil {
		t.Fatal(err)
	}
	if candidate.BuildID != 3019035 || candidate.State != azdopipeline.RunStateRunning || candidate.SourceVersion != testCommit {
		t.Fatalf("candidate = %#v", candidate)
	}

	client = &fakePipelineClient{getBuilds: []*azdopipeline.Build{{ID: 1, DefinitionID: 1151, Status: "inProgress"}}}
	if _, err := newTestService(t, client).ValidateCandidate(context.Background(), 1); err == nil {
		t.Fatal("candidate from the wrapper pipeline was accepted")
	}
}

func TestValidateRollbackSource(t *testing.T) {
	client := &fakePipelineClient{getBuilds: []*azdopipeline.Build{{
		ID: 3019035, DefinitionID: 1023, Status: "completed", Result: "succeeded",
		SourceBranch: "refs/heads/microsoft/main", SourceVersion: testCommit,
		TemplateParameters: map[string]any{
			"sourceBuildPipelineRunId": "$(Build.BuildId)",
			"publishRepoPrefix":        "public/",
		},
	}}}
	candidate, err := ValidateRollbackSource(
		context.Background(),
		client,
		VersionResolverFunc(func(context.Context, string) ([]string, error) {
			return []string{"1.26.5-2", "1.25.12-1"}, nil
		}),
		1023,
		3019035,
	)
	if err != nil {
		t.Fatal(err)
	}
	if candidate.BuildID != 3019035 || candidate.State != azdopipeline.RunStateSucceeded ||
		candidate.VersionSet != `["1.25.12-1","1.26.5-2"]` {

		t.Fatalf("candidate = %#v", candidate)
	}
}

func TestValidateRollbackSourceRejectsUnsafeBuilds(t *testing.T) {
	for _, test := range []struct {
		name  string
		build *azdopipeline.Build
	}{
		{name: "wrong definition", build: &azdopipeline.Build{
			ID: 1, DefinitionID: 1492, Status: "completed", Result: "succeeded",
			SourceBranch: "refs/heads/microsoft/main", SourceVersion: testCommit,
		}},
		{name: "failed", build: &azdopipeline.Build{
			ID: 1, DefinitionID: 1023, Status: "completed", Result: "failed",
			SourceBranch: "refs/heads/microsoft/main", SourceVersion: testCommit,
		}},
		{name: "wrong branch", build: &azdopipeline.Build{
			ID: 1, DefinitionID: 1023, Status: "completed", Result: "succeeded",
			SourceBranch: "refs/heads/feature", SourceVersion: testCommit,
		}},
		{name: "already republished", build: &azdopipeline.Build{
			ID: 1, DefinitionID: 1023, Status: "completed", Result: "succeeded",
			SourceBranch: "refs/heads/microsoft/main", SourceVersion: testCommit,
			TemplateParameters: map[string]any{"sourceBuildPipelineRunId": "123"},
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			client := &fakePipelineClient{getBuilds: []*azdopipeline.Build{test.build}}
			_, err := ValidateRollbackSource(
				context.Background(),
				client,
				VersionResolverFunc(func(context.Context, string) ([]string, error) {
					return []string{"1.26.5-2"}, nil
				}),
				1023,
				test.build.ID,
			)
			if err == nil {
				t.Fatal("unsafe rollback source was accepted")
			}
		})
	}
}

func newTestService(t *testing.T, client PipelineClient) *Service {
	t.Helper()
	return newTestServiceWithSleeper(t, client, func(context.Context, time.Duration) error { return nil })
}

func newTestServiceWithSleeper(t *testing.T, client PipelineClient, sleeper Sleeper) *Service {
	t.Helper()
	service, err := New(client, Config{
		DefinitionID: 1023,
		Versions:     []string{"1.26.5-2"},
		PollInterval: time.Millisecond,
		VersionResolver: VersionResolverFunc(func(context.Context, string) ([]string, error) {
			return []string{"1.25.12-1", "1.26.5-2"}, nil
		}),
	}, sleeper)
	if err != nil {
		t.Fatal(err)
	}
	return service
}
