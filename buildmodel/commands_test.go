// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package buildmodel

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/microsoft/go-infra/buildmodel/buildassets"
	"github.com/microsoft/go-infra/gitpr"
	"github.com/microsoft/go-infra/stringutil"
)

type dockerRoundTripFunc func(*http.Request) (*http.Response, error)

func (f dockerRoundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) {
	return f(r)
}

func TestSubmitUpdatePRRetries(t *testing.T) {
	for _, scenario := range []string{
		"branch created", "branch advanced", "branch deleted", "PR created",
		"approval failed", "auto-merge failed", "retry exhausted", "merged branch", "dry run",
		"unpublished branch", "unpublished update", "creation failed",
	} {
		t.Run(scenario, func(t *testing.T) {
			t.Setenv("GIT_AUTHOR_NAME", "Test")
			t.Setenv("GIT_AUTHOR_EMAIL", "test@example.com")
			t.Setenv("GIT_COMMITTER_NAME", "Test")
			t.Setenv("GIT_COMMITTER_EMAIL", "test@example.com")
			dir := t.TempDir()
			origin := filepath.Join(dir, "base", "images")
			head := filepath.Join(dir, "fork", "images")
			git := func(dir string, args ...string) string {
				t.Helper()
				if dir == head {
					args = append([]string{"--git-dir=" + head}, args...)
				}
				cmd := exec.Command("git", args...)
				cmd.Dir = dir
				out, err := cmd.CombinedOutput()
				if err != nil {
					t.Fatalf("git %v: %v\n%s", args, err, out)
				}
				return strings.TrimSpace(string(out))
			}
			versionsDir := filepath.Join(origin, "src", "microsoft")
			if err := os.MkdirAll(versionsDir, 0o755); err != nil {
				t.Fatal(err)
			}
			for path, content := range map[string]string{
				filepath.Join(versionsDir, "versions.json"): `{"1.42":{"version":"1.42.0"}}`,
				filepath.Join(origin, "manifest.json"):      "{}",
			} {
				if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			git(origin, "init", "-b", "main")
			git(origin, "add", ".")
			git(origin, "commit", "-m", "Initial files")
			baseCommit := git(origin, "rev-parse", "HEAD")
			git(origin, "checkout", "-b", "concurrent")
			if err := os.WriteFile(filepath.Join(origin, "concurrent-update"), []byte("keep me"), 0o600); err != nil {
				t.Fatal(err)
			}
			if scenario == "unpublished update" {
				if err := UpdateGoImagesRepo(origin, &buildassets.BuildAssets{Version: "1.42.1"}); err != nil {
					t.Fatal(err)
				}
			}
			git(origin, "add", ".")
			git(origin, "commit", "-m", "Concurrent update")
			concurrentCommit := git(origin, "rev-parse", "HEAD")
			git(dir, "init", "--bare", head)
			git(head, "fetch", origin, "main:main", "concurrent:concurrent")
			branch := "refs/heads/dev/auto-update/main"
			prExists := scenario == "branch advanced" || scenario == "branch deleted"
			if prExists || scenario == "merged branch" {
				git(head, "update-ref", branch, baseCommit)
			}
			if strings.HasPrefix(scenario, "unpublished") {
				git(head, "update-ref", branch, concurrentCommit)
			}

			oldFlags := flag.CommandLine
			flag.CommandLine = flag.NewFlagSet(scenario, flag.ContinueOnError)
			t.Cleanup(func() { flag.CommandLine = oldFlags })
			f := BindPRFlags()
			fileURL := func(path string) string {
				path = filepath.ToSlash(path)
				if filepath.VolumeName(path) != "" {
					path = "/" + path
				}
				return (&url.URL{Scheme: "file", Path: path}).String()
			}
			*f.origin, *f.to = fileURL(origin), fileURL(head)
			*f.manualBranch = "main"
			*f.tempGitDir = filepath.Join(dir, "work")
			*f.skipDockerfiles = true
			*f.dryRun = scenario == "dry run"
			*f.Auth.GitHubPat = "test-token"
			*f.ReviewerAuth.GitHubPat = "test-reviewer-token"
			*f.buildAssetJSON = filepath.Join(dir, "assets.json")
			if err := stringutil.WriteJSONFile(*f.buildAssetJSON, &buildassets.BuildAssets{
				Branch: "release-branch.go1.42", Version: "1.42.1",
			}); err != nil {
				t.Fatal(err)
			}
			parsedOrigin, err := gitpr.ParseRemoteURL(*f.origin)
			if err != nil {
				t.Fatal(err)
			}
			checks, posts, approvals, merges := 0, 0, 0, 0
			oldTransport := http.DefaultTransport
			t.Cleanup(func() { http.DefaultTransport = oldTransport })
			http.DefaultTransport = dockerRoundTripFunc(func(r *http.Request) (*http.Response, error) {
				status, body := http.StatusOK, `{}`
				switch {
				case r.URL.Path == "/user":
					body = `{"login":"bot"}`
				case strings.HasSuffix(r.URL.Path, "/pulls"):
					posts++
					prExists = true
					if scenario == "creation failed" && posts == 1 {
						prExists = false
						status = http.StatusInternalServerError
					} else if scenario == "PR created" {
						status = http.StatusUnprocessableEntity
						body = `{"message":"Validation Failed","errors":[{"resource":"PullRequest","code":"custom","message":"A pull request already exists for fork:dev/auto-update/main."}]}`
					} else {
						status = http.StatusCreated
						body = `{"number":42,"node_id":"PR_node","html_url":"https://example.com/pull/42"}`
					}
				case r.URL.Path == "/graphql":
					var request struct{ Query string }
					if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
						t.Fatal(err)
					}
					switch {
					case strings.Contains(request.Query, "pullRequests("):
						checks++
						found := prExists
						if checks == 1 {
							switch scenario {
							case "branch created", "branch advanced":
								git(head, "update-ref", branch, concurrentCommit)
								prExists = true
							case "branch deleted":
								git(head, "update-ref", "-d", branch)
								prExists = false
							}
						}
						if scenario == "retry exhausted" {
							// Change the branch after every observation, so every lease fails.
							commit := baseCommit
							if checks%2 == 1 {
								commit = concurrentCommit
							}
							git(head, "update-ref", branch, commit)
						}
						nodes := "[]"
						if found {
							nodes = fmt.Sprintf(`[{"id":"PR_node","number":42,"author":{"login":"bot"},"headRepositoryOwner":{"login":"fork"},"baseRepository":{"owner":{"login":"base"},"nameWithOwner":%q}}]`, parsedOrigin.GetOwnerSlashRepo())
						}
						body = `{"data":{"repository":{"pullRequests":{"nodes":` + nodes + `}}}}`
					case strings.Contains(request.Query, "addPullRequestReview"):
						approvals++
						if scenario == "approval failed" && approvals == 1 {
							body = `{"errors":[{"message":"temporary review failure"}]}`
						}
					case strings.Contains(request.Query, "enablePullRequestAutoMerge"):
						merges++
						if scenario == "auto-merge failed" && merges == 1 {
							body = `{"errors":[{"message":"temporary auto-merge failure"}]}`
						}
					default:
						t.Fatalf("unexpected GraphQL request: %s", request.Query)
					}
				default:
					t.Fatalf("unexpected request: %s", r.URL)
				}
				return &http.Response{
					StatusCode: status,
					Header:     make(http.Header),
					Body:       io.NopCloser(strings.NewReader(body)),
					Request:    r,
				}, nil
			})

			err = SubmitUpdatePR(f)
			if scenario == "retry exhausted" {
				if err == nil || checks != 5 || posts != 0 {
					t.Fatalf("exhausted retries: error %v, checks %d, posts %d", err, checks, posts)
				}
				if got := git(head, "rev-parse", branch); got != concurrentCommit {
					t.Fatalf("overwrote concurrent update: %s", got)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			wantChecks := 2
			if scenario == "dry run" || scenario == "merged branch" || strings.HasPrefix(scenario, "unpublished") {
				wantChecks = 1
			}
			if checks != wantChecks {
				t.Errorf("PR checks = %d, want %d", checks, wantChecks)
			}
			if scenario == "dry run" {
				if posts != 0 || approvals != 0 || merges != 0 || git(head, "for-each-ref", "--format=%(refname)", branch) != "" {
					t.Fatal("dry run modified the remote or PR")
				}
				return
			}
			if approvals == 0 || merges == 0 {
				t.Fatalf("PR not finalized: approvals %d, auto-merges %d", approvals, merges)
			}
			if scenario == "approval failed" && approvals != 2 || scenario == "auto-merge failed" && merges != 2 {
				t.Fatalf("failed finalization not retried: approvals %d, auto-merges %d", approvals, merges)
			}
			if got := git(head, "show", branch+":src/microsoft/versions.json"); !strings.Contains(got, "1.42.1") {
				t.Fatalf("version was not updated: %s", got)
			}
			if scenario == "branch created" || scenario == "branch advanced" || strings.HasPrefix(scenario, "unpublished") {
				if got := git(head, "show", branch+":concurrent-update"); got != "keep me" {
					t.Fatalf("concurrent update lost: %s", got)
				}
			}
			if got := git(origin, "rev-parse", "main"); got != baseCommit {
				t.Fatalf("modified base branch: %s", got)
			}
			if got := git(origin, "for-each-ref", "--format=%(refname)", branch); got != "" {
				t.Fatalf("pushed to origin instead of head repository: %s", got)
			}
		})
	}
}

func TestSubmitUpdatePRSkips(t *testing.T) {
	t.Setenv("BUILD_BUILDID", "123")
	for _, tt := range []struct {
		name           string
		branch         string
		versions       string
		manifest       string
		wantErr        bool
		setBuildNumber bool
	}{
		{"unmatched version", "release-branch.go1.42", "{}", "{}", false, true},
		{"number disabled", "release-branch.go1.42", "{}", "{}", false, false},
		{"unmapped branch", "feature/test", "{}", "{}", false, true},
		{"literal percent escape", "feature/%0Atest", "{}", "{}", false, true},
		{"invalid versions", "release-branch.go1.42", "{", "{}", true, true},
		{"invalid manifest", "release-branch.go1.42", "{}", "{", true, true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			origin := filepath.Join(dir, "origin")
			versionsDir := filepath.Join(origin, "src", "microsoft")
			if err := os.MkdirAll(versionsDir, 0o755); err != nil {
				t.Fatal(err)
			}
			for path, content := range map[string]string{
				filepath.Join(versionsDir, "versions.json"): tt.versions,
				filepath.Join(origin, "manifest.json"):      tt.manifest,
			} {
				if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			for _, args := range [][]string{
				{"init", "-b", "microsoft/nightly"},
				{"add", "."},
				{"-c", "user.name=Test", "-c", "user.email=test@example.com", "commit", "-m", "Initial files"},
			} {
				cmd := exec.Command("git", args...)
				cmd.Dir = origin
				if out, err := cmd.CombinedOutput(); err != nil {
					t.Fatalf("git %v: %v\n%s", args, err, out)
				}
			}
			assetPath := filepath.Join(dir, "assets.json")
			if err := stringutil.WriteJSONFile(assetPath, &buildassets.BuildAssets{
				Branch: tt.branch, Version: "1.42.1",
			}); err != nil {
				t.Fatal(err)
			}

			oldFlags := flag.CommandLine
			flag.CommandLine = flag.NewFlagSet(tt.name, flag.ContinueOnError)
			defer func() { flag.CommandLine = oldFlags }()
			f := BindPRFlags()
			originPath := filepath.ToSlash(origin)
			if filepath.VolumeName(origin) != "" {
				originPath = "/" + originPath
			}
			*f.origin = (&url.URL{Scheme: "file", Path: originPath}).String()
			*f.tempGitDir = filepath.Join(dir, "work")
			*f.buildAssetJSON = assetPath
			*f.skipDockerfiles = true
			*f.dryRun = true
			*f.setBuildNumber = tt.setBuildNumber
			*f.SetVariablePRNumber = "PRNumber"
			*f.SetVariableUpToDateCommit = "Commit"

			if tt.name == "unmapped branch" {
				*f.skipDockerfiles = false
				t.Setenv("PATH", "")
			}

			output, err := os.Create(filepath.Join(dir, "stdout"))
			if err != nil {
				t.Fatal(err)
			}
			defer output.Close()
			oldStdout := os.Stdout
			os.Stdout = output
			defer func() { os.Stdout = oldStdout }()
			err = SubmitUpdatePR(f)
			os.Stdout = oldStdout
			if tt.name == "unmapped branch" {
				if _, err := os.Stat(*f.tempGitDir); !os.IsNotExist(err) {
					t.Fatalf("unmapped branch created a work directory or stat failed: %v", err)
				}
			}
			if (err != nil) != tt.wantErr {
				t.Fatalf("SubmitUpdatePR() error = %v, wantErr %v", err, tt.wantErr)
			}
			data, err := os.ReadFile(output.Name())
			if err != nil {
				t.Fatal(err)
			}
			got := string(data)
			wantNumber := "##vso[build.updatebuildnumber]123_" +
				strings.NewReplacer("/", "_", "%", "%AZP25").Replace(tt.branch) + "_1.42.1\n"
			if strings.Contains(got, wantNumber) != tt.setBuildNumber {
				t.Errorf("unexpected build number output:\n%s", got)
			}
			for _, want := range []string{
				"##vso[task.setvariable variable=PRNumber]nil\n",
				"##vso[task.setvariable variable=Commit]nil\n",
			} {
				if strings.Contains(got, want) == tt.wantErr {
					t.Errorf("unexpected skip output for %q:\n%s", want, got)
				}
			}
		})
	}
}
