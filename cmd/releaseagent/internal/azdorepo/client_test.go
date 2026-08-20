// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package azdorepo

import (
	"context"
	"errors"
	"strings"
	"testing"

	azdogit "github.com/microsoft/azure-devops-go-api/azuredevops/git"
)

type staticToken string

func (t staticToken) Token(context.Context) (string, error) { return string(t), nil }

type fakeGitClient struct {
	getCommit func(context.Context, azdogit.GetCommitArgs) (*azdogit.GitCommit, error)
	getItem   func(context.Context, azdogit.GetItemArgs) (*azdogit.GitItem, error)
	getRefs   func(context.Context, azdogit.GetRefsArgs) (*azdogit.GetRefsResponseValue, error)
}

func (f *fakeGitClient) GetCommit(ctx context.Context, args azdogit.GetCommitArgs) (*azdogit.GitCommit, error) {
	if f.getCommit == nil {
		return nil, errors.New("unexpected GetCommit call")
	}
	return f.getCommit(ctx, args)
}

func (f *fakeGitClient) GetItem(ctx context.Context, args azdogit.GetItemArgs) (*azdogit.GitItem, error) {
	if f.getItem == nil {
		return nil, errors.New("unexpected GetItem call")
	}
	return f.getItem(ctx, args)
}

func (f *fakeGitClient) GetRefs(ctx context.Context, args azdogit.GetRefsArgs) (*azdogit.GetRefsResponseValue, error) {
	if f.getRefs == nil {
		return nil, errors.New("unexpected GetRefs call")
	}
	return f.getRefs(ctx, args)
}

func newTestClient(t *testing.T, sdk *fakeGitClient) *Client {
	t.Helper()
	client, err := NewClient("https://example.invalid", "internal", "microsoft-go-images", staticToken("test-token"))
	if err != nil {
		t.Fatal(err)
	}
	client.newGitClient = func(context.Context) (gitClient, string, error) {
		return sdk, "test-token", nil
	}
	return client
}

func pointer[T any](value T) *T { return &value }

func TestGetJSONFileAtCommit(t *testing.T) {
	const commit = "81ce9afc2b75ec4e153dd15fc3c7539b12024945"
	client := newTestClient(t, &fakeGitClient{getItem: func(_ context.Context, args azdogit.GetItemArgs) (*azdogit.GitItem, error) {
		if args.Project == nil || *args.Project != "internal" || args.RepositoryId == nil || *args.RepositoryId != "microsoft-go-images" ||
			args.Path == nil || *args.Path != "/src/microsoft/versions.json" || args.VersionDescriptor == nil ||
			args.VersionDescriptor.Version == nil || *args.VersionDescriptor.Version != commit ||
			args.VersionDescriptor.VersionType == nil || *args.VersionDescriptor.VersionType != azdogit.GitVersionTypeValues.Commit ||
			args.IncludeContent == nil || !*args.IncludeContent {

			t.Fatalf("item args = %#v", args)
		}
		return &azdogit.GitItem{Content: pointer(`{"1.26":{"version":"1.26.5","revision":"2"}}`)}, nil
	}})
	var model map[string]struct {
		Version  string `json:"version"`
		Revision string `json:"revision"`
	}
	if err := client.GetJSONFileAtCommit(context.Background(), "/src/microsoft/versions.json", commit, &model); err != nil {
		t.Fatal(err)
	}
	if model["1.26"].Version != "1.26.5" || model["1.26"].Revision != "2" {
		t.Fatalf("model = %#v", model)
	}
}

func TestGetFileAtBranch(t *testing.T) {
	client := newTestClient(t, &fakeGitClient{getItem: func(_ context.Context, args azdogit.GetItemArgs) (*azdogit.GitItem, error) {
		if args.Path == nil || *args.Path != "/eng/pipeline/go-docker-rolling-internal-pipeline.yml" ||
			args.VersionDescriptor == nil || args.VersionDescriptor.Version == nil || *args.VersionDescriptor.Version != "microsoft/main" ||
			args.VersionDescriptor.VersionType == nil || *args.VersionDescriptor.VersionType != azdogit.GitVersionTypeValues.Branch {

			t.Fatalf("item args = %#v", args)
		}
		return &azdogit.GitItem{Content: pointer("parameters:\n")}, nil
	}})
	data, err := client.GetFileAtBranch(
		context.Background(),
		"/eng/pipeline/go-docker-rolling-internal-pipeline.yml",
		"refs/heads/microsoft/main",
	)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "parameters:\n" {
		t.Fatalf("content = %q", data)
	}
}

func TestVerifyCommit(t *testing.T) {
	const commit = "81ce9afc2b75ec4e153dd15fc3c7539b12024945"
	client := newTestClient(t, &fakeGitClient{getCommit: func(_ context.Context, args azdogit.GetCommitArgs) (*azdogit.GitCommit, error) {
		if args.CommitId == nil || *args.CommitId != commit || args.Project == nil || *args.Project != "internal" ||
			args.RepositoryId == nil || *args.RepositoryId != "microsoft-go-images" {

			t.Fatalf("commit args = %#v", args)
		}
		return &azdogit.GitCommit{CommitId: pointer(strings.ToUpper(commit))}, nil
	}})
	if err := client.VerifyCommit(context.Background(), commit); err != nil {
		t.Fatal(err)
	}
}

func TestGetBranchTip(t *testing.T) {
	const commit = "81ce9afc2b75ec4e153dd15fc3c7539b12024945"
	client := newTestClient(t, &fakeGitClient{getRefs: func(_ context.Context, args azdogit.GetRefsArgs) (*azdogit.GetRefsResponseValue, error) {
		if args.Filter == nil || *args.Filter != "heads/microsoft/main" {
			t.Fatalf("refs args = %#v", args)
		}
		return &azdogit.GetRefsResponseValue{Value: []azdogit.GitRef{{
			Name: pointer("refs/heads/microsoft/main"), ObjectId: pointer(strings.ToUpper(commit)),
		}}}, nil
	}})
	tip, err := client.GetBranchTip(context.Background(), "refs/heads/microsoft/main")
	if err != nil {
		t.Fatal(err)
	}
	if tip.Name != "refs/heads/microsoft/main" || tip.ObjectID != commit {
		t.Fatalf("tip = %#v", tip)
	}
}

func TestGetBranchTipRejectsUnexpectedResponse(t *testing.T) {
	for _, test := range []struct {
		name  string
		value []azdogit.GitRef
	}{
		{name: "missing"},
		{name: "ambiguous", value: []azdogit.GitRef{
			{Name: pointer("refs/heads/microsoft/main"), ObjectId: pointer("81ce9afc2b75ec4e153dd15fc3c7539b12024945")},
			{Name: pointer("refs/heads/microsoft/main-old"), ObjectId: pointer("2ef65db89e42942c24e3d8f0b8a8eb52bc86857a")},
		}},
		{name: "wrong ref", value: []azdogit.GitRef{{
			Name: pointer("refs/heads/main"), ObjectId: pointer("81ce9afc2b75ec4e153dd15fc3c7539b12024945"),
		}}},
		{name: "malformed commit", value: []azdogit.GitRef{{
			Name: pointer("refs/heads/microsoft/main"), ObjectId: pointer("not-a-commit"),
		}}},
	} {
		t.Run(test.name, func(t *testing.T) {
			client := newTestClient(t, &fakeGitClient{getRefs: func(context.Context, azdogit.GetRefsArgs) (*azdogit.GetRefsResponseValue, error) {
				return &azdogit.GetRefsResponseValue{Value: test.value}, nil
			}})
			if _, err := client.GetBranchTip(context.Background(), "microsoft/main"); err == nil {
				t.Fatal("unexpected branch response was accepted")
			}
		})
	}
}

func TestGetJSONFileAtCommitRedactsToken(t *testing.T) {
	client := newTestClient(t, &fakeGitClient{getItem: func(context.Context, azdogit.GetItemArgs) (*azdogit.GitItem, error) {
		return nil, errors.New("denied test-token")
	}})
	err := client.GetJSONFileAtCommit(context.Background(), "/src/microsoft/versions.json", "81ce9afc2b75ec4e153dd15fc3c7539b12024945", &map[string]any{})
	if err == nil || strings.Contains(err.Error(), "test-token") || !strings.Contains(err.Error(), "[REDACTED]") {
		t.Fatalf("error = %v", err)
	}
}
