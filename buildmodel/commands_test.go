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

func TestDockerRemoteGitHelper(t *testing.T) {
	if secret := os.Getenv("DOCKER_GIT_TEST_SECRET"); secret != "" {
		fmt.Fprintln(os.Stdout, secret)
		fmt.Fprintln(os.Stderr, secret)
		if os.Getenv("DOCKER_GIT_TEST_FAIL") == "1" {
			os.Exit(1)
		}
		os.Exit(0)
	}
}

func TestDockerRemoteGitCredentials(t *testing.T) {
	const secret = "dummy-credential-for-log-test"
	dir := t.TempDir()
	output, err := os.Create(filepath.Join(dir, "output"))
	if err != nil {
		t.Fatal(err)
	}
	defer output.Close()
	oldStdout, oldStderr := os.Stdout, os.Stderr
	os.Stdout, os.Stderr = output, output
	defer func() { os.Stdout, os.Stderr = oldStdout, oldStderr }()
	for _, fail := range []string{"0", "1"} {
		cmd := exec.Command(os.Args[0], "-test.run=^TestDockerRemoteGitHelper$")
		cmd.Env = append(os.Environ(), "DOCKER_GIT_TEST_SECRET="+secret, "DOCKER_GIT_TEST_FAIL="+fail)
		err := runDockerRemoteGit(cmd)
		if (err != nil) != (fail == "1") {
			t.Fatalf("remote Git error = %v, fail = %s", err, fail)
		}
		if err != nil && strings.Contains(err.Error(), secret) {
			t.Fatal("remote Git error leaked credentials")
		}
	}
	// Rewrite a credential-bearing URL to a local repository, avoiding network
	// access while exercising successful and failing ls-remote commands.
	cmd := exec.Command("git", "init", dir)
	if err := cmd.Run(); err != nil {
		t.Fatal(err)
	}
	remote := "https://user:" + secret + "@example.invalid/repo"
	t.Setenv("GIT_CONFIG_COUNT", "1")
	t.Setenv("GIT_CONFIG_KEY_0", "url."+filepath.ToSlash(dir)+"/.insteadOf")
	t.Setenv("GIT_CONFIG_VALUE_0", remote)
	if _, err := dockerPRBranchHead(dir, remote, "main"); err != nil {
		t.Fatal(err)
	}
	// Force Git to print a diagnostic containing the authenticated remote.
	t.Setenv("GIT_CONFIG_KEY_0", "url."+filepath.ToSlash(filepath.Join(dir, "missing"))+"/.insteadOf")
	if _, err := dockerPRBranchHead(dir, remote, "main"); err == nil || strings.Contains(err.Error(), secret) {
		t.Fatalf("unsafe or missing lookup error: %v", err)
	}
	os.Stdout, os.Stderr = oldStdout, oldStderr
	data, err := os.ReadFile(output.Name())
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), secret) {
		t.Fatalf("credentials leaked in logs: %s", data)
	}
}

const dockerUpdateBranch = "refs/heads/dev/auto-update/main"

type dockerPRFixture struct {
	t                                *testing.T
	dir, origin, head                string
	baseCommit, concurrentCommit     string
	initialHead, version             string
	flags                            *PRFlags
	prExists, duplicatePR            bool
	checks, posts, approvals, merges int
	createFailures                   int
	approvalFailures, mergeFailures  int
	afterCheck                       func()
}

func newDockerPRFixture(t *testing.T) *dockerPRFixture {
	t.Helper()
	t.Setenv("GIT_AUTHOR_NAME", "Test")
	t.Setenv("GIT_AUTHOR_EMAIL", "test@example.com")
	t.Setenv("GIT_COMMITTER_NAME", "Test")
	t.Setenv("GIT_COMMITTER_EMAIL", "test@example.com")
	f := &dockerPRFixture{t: t, dir: t.TempDir(), version: "1.42.1"}
	f.origin = filepath.Join(f.dir, "base", "images")
	f.head = filepath.Join(f.dir, "fork", "images")
	if err := os.MkdirAll(filepath.Join(f.origin, "src", "microsoft"), 0o755); err != nil {
		t.Fatal(err)
	}
	f.write("src/microsoft/versions.json", `{"1.42":{"version":"1.42.0"}}`)
	f.write("manifest.json", "{}")
	f.git(f.origin, "init", "-b", "main")
	f.baseCommit = f.commit("Initial files")
	f.git(f.origin, "checkout", "-b", "concurrent")
	f.write("concurrent-update", "keep me")
	f.concurrentCommit = f.commit("Concurrent update")
	f.git(f.dir, "init", "--bare", f.head)

	oldFlags := flag.CommandLine
	flag.CommandLine = flag.NewFlagSet(t.Name(), flag.ContinueOnError)
	t.Cleanup(func() { flag.CommandLine = oldFlags })
	f.flags = BindPRFlags()
	fileURL := func(path string) string {
		path = filepath.ToSlash(path)
		if filepath.VolumeName(path) != "" {
			path = "/" + path
		}
		return (&url.URL{Scheme: "file", Path: path}).String()
	}
	*f.flags.origin, *f.flags.to = fileURL(f.origin), fileURL(f.head)
	*f.flags.manualBranch = "main"
	*f.flags.tempGitDir = filepath.Join(f.dir, "work")
	*f.flags.skipDockerfiles = true
	*f.flags.Auth.GitHubPat = "test-token"
	*f.flags.ReviewerAuth.GitHubPat = "test-reviewer-token"
	*f.flags.buildAssetJSON = filepath.Join(f.dir, "assets.json")
	*f.flags.SetVariablePRNumber = "PRNumber"
	*f.flags.SetVariableUpToDateCommit = "Commit"
	return f
}

func (f *dockerPRFixture) git(dir string, args ...string) string {
	f.t.Helper()
	if dir == f.head {
		args = append([]string{"--git-dir=" + f.head}, args...)
	}
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		f.t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

func (f *dockerPRFixture) write(path, content string) {
	f.t.Helper()
	if err := os.WriteFile(filepath.Join(f.origin, path), []byte(content), 0o600); err != nil {
		f.t.Fatal(err)
	}
}

func (f *dockerPRFixture) commit(message string) string {
	f.t.Helper()
	f.git(f.origin, "add", ".")
	f.git(f.origin, "commit", "-m", message)
	return f.git(f.origin, "rev-parse", "HEAD")
}

func (f *dockerPRFixture) updateVersion(version string) string {
	f.t.Helper()
	if err := UpdateGoImagesRepo(f.origin, &buildassets.BuildAssets{Version: version}); err != nil {
		f.t.Fatal(err)
	}
	return f.commit("Update version to " + version)
}

func (f *dockerPRFixture) roundTrip(r *http.Request) (*http.Response, error) {
	status, body := http.StatusOK, `{}`
	switch {
	case r.URL.Path == "/user":
		body = `{"login":"bot"}`
	case strings.HasSuffix(r.URL.Path, "/pulls"):
		f.posts++
		f.prExists = true
		switch {
		case f.posts <= f.createFailures:
			f.prExists = false
			status = http.StatusInternalServerError
		case f.duplicatePR:
			status = http.StatusUnprocessableEntity
			body = `{"message":"Validation Failed","errors":[{"resource":"PullRequest","code":"custom","message":"A pull request already exists for fork:dev/auto-update/main."}]}`
		default:
			status = http.StatusCreated
			body = `{"number":42,"node_id":"PR_node","html_url":"https://example.com/pull/42"}`
		}
	case r.URL.Path == "/graphql":
		var request struct{ Query string }
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			f.t.Fatal(err)
		}
		switch {
		case strings.Contains(request.Query, "pullRequests("):
			f.checks++
			found := f.prExists
			if f.afterCheck != nil {
				f.afterCheck()
			}
			nodes := "[]"
			if found {
				origin, err := gitpr.ParseRemoteURL(*f.flags.origin)
				if err != nil {
					f.t.Fatal(err)
				}
				nodes = fmt.Sprintf(`[{"id":"PR_node","number":42,"author":{"login":"bot"},"headRepositoryOwner":{"login":"fork"},"baseRepository":{"owner":{"login":"base"},"nameWithOwner":%q}}]`, origin.GetOwnerSlashRepo())
			}
			body = `{"data":{"repository":{"pullRequests":{"nodes":` + nodes + `}}}}`
		case strings.Contains(request.Query, "addPullRequestReview"):
			f.approvals++
			if f.approvals <= f.approvalFailures {
				body = `{"errors":[{"message":"temporary review failure"}]}`
			}
		case strings.Contains(request.Query, "enablePullRequestAutoMerge"):
			f.merges++
			if f.merges <= f.mergeFailures {
				body = `{"errors":[{"message":"temporary auto-merge failure"}]}`
			}
		default:
			f.t.Fatalf("unexpected GraphQL request: %s", request.Query)
		}
	default:
		f.t.Fatalf("unexpected request: %s", r.URL)
	}
	return &http.Response{
		StatusCode: status, Header: make(http.Header),
		Body: io.NopCloser(strings.NewReader(body)), Request: r,
	}, nil
}

func (f *dockerPRFixture) run() (string, error) {
	f.t.Helper()
	f.git(f.head, "fetch", f.origin, "refs/heads/main:refs/heads/main", "refs/heads/concurrent:refs/heads/concurrent")
	if f.initialHead != "" {
		f.git(f.head, "update-ref", dockerUpdateBranch, f.initialHead)
	}
	if err := stringutil.WriteJSONFile(*f.flags.buildAssetJSON, &buildassets.BuildAssets{
		Branch: "release-branch.go1.42", Version: f.version,
	}); err != nil {
		f.t.Fatal(err)
	}
	oldTransport := http.DefaultTransport
	http.DefaultTransport = dockerRoundTripFunc(f.roundTrip)
	defer func() { http.DefaultTransport = oldTransport }()
	output, err := os.Create(filepath.Join(f.dir, "stdout"))
	if err != nil {
		f.t.Fatal(err)
	}
	defer output.Close()
	oldStdout := os.Stdout
	os.Stdout = output
	defer func() { os.Stdout = oldStdout }()
	err = SubmitUpdatePR(f.flags)
	os.Stdout = oldStdout
	data, readErr := os.ReadFile(output.Name())
	if readErr != nil {
		f.t.Fatal(readErr)
	}
	if err != nil && strings.Contains(string(data), "##vso[task.setvariable") {
		f.t.Fatalf("failed run published pipeline variables:\n%s", data)
	}
	return string(data), err
}

func (f *dockerPRFixture) assertUpdated(wantChecks int) {
	f.t.Helper()
	if f.checks != wantChecks {
		f.t.Errorf("PR checks = %d, want %d", f.checks, wantChecks)
	}
	if f.approvals == 0 || f.merges == 0 {
		f.t.Fatalf("PR not finalized: approvals %d, auto-merges %d", f.approvals, f.merges)
	}
	if got := f.git(f.head, "show", dockerUpdateBranch+":src/microsoft/versions.json"); !strings.Contains(got, f.version) {
		f.t.Fatalf("version was not updated: %s", got)
	}
	if got := f.git(f.origin, "rev-parse", "refs/heads/main"); got != f.baseCommit {
		f.t.Fatalf("modified base branch: %s", got)
	}
	if got := f.git(f.origin, "for-each-ref", "--format=%(refname)", dockerUpdateBranch); got != "" {
		f.t.Fatalf("pushed to origin instead of head repository: %s", got)
	}
}

func (f *dockerPRFixture) assertFile(path, want string) {
	f.t.Helper()
	if got := f.git(f.head, "show", dockerUpdateBranch+":"+path); got != want {
		f.t.Fatalf("%s contents = %q, want %q", path, got, want)
	}
}

func TestSubmitUpdatePRReconciliation(t *testing.T) {
	for _, tt := range []struct {
		name           string
		setup          func(*dockerPRFixture)
		wantChecks     int
		keepConcurrent bool
	}{
		{"branch created", func(f *dockerPRFixture) {
			f.afterCheck = func() {
				if f.checks == 1 {
					f.git(f.head, "update-ref", dockerUpdateBranch, f.concurrentCommit)
					f.prExists = true
				}
			}
		}, 2, true},
		{"branch advanced", func(f *dockerPRFixture) {
			f.initialHead, f.prExists = f.baseCommit, true
			f.afterCheck = func() {
				if f.checks == 1 {
					f.git(f.head, "update-ref", dockerUpdateBranch, f.concurrentCommit)
				}
			}
		}, 2, true},
		{"branch deleted", func(f *dockerPRFixture) {
			f.initialHead, f.prExists = f.baseCommit, true
			f.afterCheck = func() {
				if f.checks == 1 {
					f.git(f.head, "update-ref", "-d", dockerUpdateBranch)
					f.prExists = false
				}
			}
		}, 2, false},
		{"PR created", func(f *dockerPRFixture) { f.duplicatePR = true }, 2, false},
		{"merged branch", func(f *dockerPRFixture) { f.initialHead = f.baseCommit }, 1, false},
		{"unpublished branch", func(f *dockerPRFixture) { f.initialHead = f.concurrentCommit }, 1, true},
		{"unpublished update", func(f *dockerPRFixture) {
			f.concurrentCommit = f.updateVersion("1.42.1")
			f.initialHead = f.concurrentCommit
		}, 1, true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			f := newDockerPRFixture(t)
			tt.setup(f)
			if _, err := f.run(); err != nil {
				t.Fatal(err)
			}
			f.assertUpdated(tt.wantChecks)
			if tt.keepConcurrent {
				f.assertFile("concurrent-update", "keep me")
			}
		})
	}
	t.Run("dry run", func(t *testing.T) {
		f := newDockerPRFixture(t)
		*f.flags.dryRun = true
		if _, err := f.run(); err != nil {
			t.Fatal(err)
		}
		if f.checks != 1 {
			t.Errorf("PR checks = %d, want 1", f.checks)
		}
		if f.posts != 0 || f.approvals != 0 || f.merges != 0 || f.git(f.head, "for-each-ref", "--format=%(refname)", dockerUpdateBranch) != "" {
			t.Fatal("dry run modified the remote or PR")
		}
	})
}

func TestSubmitUpdatePRFinalizationRetries(t *testing.T) {
	for _, tt := range []struct {
		name                                            string
		createFailures, approvalFailures, mergeFailures int
		wantPosts, wantApprovals, wantMerges            int
		exhausted                                       bool
	}{
		{"creation failed", 1, 0, 0, 2, 1, 1, false},
		{"approval failed", 0, 1, 0, 1, 2, 1, false},
		{"auto-merge failed", 0, 0, 1, 1, 2, 2, false},
		{"approval exhausted", 0, 5, 0, 1, 5, 0, true},
		{"auto-merge exhausted", 0, 0, 5, 1, 5, 5, true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			f := newDockerPRFixture(t)
			f.createFailures, f.approvalFailures, f.mergeFailures = tt.createFailures, tt.approvalFailures, tt.mergeFailures
			_, err := f.run()
			if f.posts != tt.wantPosts || f.approvals != tt.wantApprovals || f.merges != tt.wantMerges {
				t.Fatalf("PR operations: posts %d, approvals %d, merges %d; want %d, %d, %d",
					f.posts, f.approvals, f.merges, tt.wantPosts, tt.wantApprovals, tt.wantMerges)
			}
			if tt.exhausted {
				if err == nil || f.checks != 5 {
					t.Fatalf("finalization retries: error %v, checks %d", err, f.checks)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			f.assertUpdated(2)
		})
	}
}

func TestSubmitUpdatePRConflictingUpdates(t *testing.T) {
	for _, tt := range []struct {
		name  string
		setup func(*dockerPRFixture)
	}{
		{"retry exhausted", func(f *dockerPRFixture) {
			// Change the branch after every observation, so every lease fails.
			f.afterCheck = func() {
				commit := f.baseCommit
				if f.checks%2 == 1 {
					commit = f.concurrentCommit
				}
				f.git(f.head, "update-ref", dockerUpdateBranch, commit)
			}
		}},
		{"unpublished conflict", func(f *dockerPRFixture) {
			f.concurrentCommit = f.updateVersion("1.42.1")
			f.initialHead = f.concurrentCommit
			f.git(f.origin, "checkout", "main")
			f.baseCommit = f.updateVersion("1.42.2")
		}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			f := newDockerPRFixture(t)
			tt.setup(f)
			if _, err := f.run(); err == nil || f.checks != 5 || f.posts != 0 {
				t.Fatalf("exhausted retries: error %v, checks %d, posts %d", err, f.checks, f.posts)
			}
			if got := f.git(f.head, "rev-parse", dockerUpdateBranch); got != f.concurrentCommit {
				t.Fatalf("overwrote concurrent update: %s", got)
			}
		})
	}
}

func TestSubmitUpdatePRStaleUpdate(t *testing.T) {
	f := newDockerPRFixture(t)
	f.concurrentCommit = f.updateVersion("1.42.2")
	f.afterCheck = func() {
		if f.checks == 1 {
			f.git(f.head, "update-ref", dockerUpdateBranch, f.concurrentCommit)
			f.prExists = true
		}
	}
	if _, err := f.run(); err == nil || f.checks != 5 || f.posts != 0 {
		t.Fatalf("exhausted retries: error %v, checks %d, posts %d", err, f.checks, f.posts)
	}
	if got := f.git(f.head, "rev-parse", dockerUpdateBranch); got != f.concurrentCommit {
		t.Fatalf("overwrote concurrent update: %s", got)
	}
}

func TestSubmitUpdatePRSquashReconciliation(t *testing.T) {
	for _, tt := range []struct {
		name          string
		unrelatedBase bool
		advanceBase   bool
		setup         func(*dockerPRFixture)
		preserved     map[string]string
	}{
		{name: "squash-merged update"},
		{name: "squash-merged advanced base", advanceBase: true},
		{
			name: "squash-merged unpublished commit", advanceBase: true,
			setup: func(f *dockerPRFixture) {
				f.write("unpublished", "preserve this update")
				f.concurrentCommit = f.commit("Unpublished update after squash merge")
			}, preserved: map[string]string{"unpublished": "preserve this update"},
		},
		{
			name: "squash-merged unpublished merge", advanceBase: true,
			setup: func(f *dockerPRFixture) {
				f.write("unpublished", "preserve this update")
				f.commit("Unpublished update after squash merge")
				f.git(f.origin, "checkout", "-b", "side", f.concurrentCommit)
				f.git(f.origin, "commit", "--allow-empty", "-m", "Side commit")
				f.git(f.origin, "checkout", "concurrent")
				f.git(f.origin, "merge", "--no-ff", "--no-edit", "side")
				f.write("merge-amendment", "preserve merge contents")
				f.git(f.origin, "add", ".")
				f.git(f.origin, "commit", "--amend", "--no-edit")
				f.concurrentCommit = f.git(f.origin, "rev-parse", "HEAD")
			}, preserved: map[string]string{"unpublished": "preserve this update", "merge-amendment": "preserve merge contents"},
		},
		{
			name: "squash-merged unrelated base", unrelatedBase: true, advanceBase: true,
			setup: func(f *dockerPRFixture) {
				f.write("unpublished", "preserve this update")
				f.concurrentCommit = f.commit("Unpublished update after squash merge")
			}, preserved: map[string]string{"unpublished": "preserve this update", "README": "base-only change"},
		},
		{
			name: "squash-merged Git warnings", advanceBase: true,
			setup: func(f *dockerPRFixture) {
				f.t.Setenv("GIT_CONFIG_COUNT", "1")
				f.t.Setenv("GIT_CONFIG_KEY_0", "core.fsyncObjectFiles")
				f.t.Setenv("GIT_CONFIG_VALUE_0", "false")
			},
		},
		{
			name: "squash-merged tag collision", advanceBase: true,
			setup: func(f *dockerPRFixture) {
				f.git(f.origin, "tag", "main", f.baseCommit+"^")
			},
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			f := newDockerPRFixture(t)
			f.concurrentCommit = f.updateVersion("1.42.1")
			if tt.unrelatedBase {
				f.git(f.origin, "checkout", "main")
				f.write("README", "base-only change")
				f.commit("Unrelated base change")
				f.git(f.origin, "merge", "--squash", "concurrent")
				f.baseCommit = f.commit("Squash merge")
			} else {
				f.baseCommit = f.git(f.origin, "commit-tree", f.concurrentCommit+"^{tree}", "-p", f.baseCommit, "-m", "Squash merge")
				f.git(f.origin, "update-ref", "refs/heads/main", f.baseCommit)
			}
			if tt.advanceBase {
				f.git(f.origin, "checkout", "main")
				f.baseCommit = f.updateVersion("1.42.2")
				f.version = "1.42.3"
			}
			f.git(f.origin, "checkout", "concurrent")
			if tt.setup != nil {
				tt.setup(f)
			}
			f.initialHead = f.concurrentCommit
			output, err := f.run()
			// Keep injected Git diagnostics out of assertion command output.
			t.Setenv("GIT_CONFIG_COUNT", "0")
			if err != nil {
				t.Fatal(err)
			}
			if !tt.advanceBase {
				if !strings.Contains(output, "##vso[task.setvariable variable=Commit]"+f.baseCommit+"\n") {
					t.Fatalf("did not report remote base commit: %s", output)
				}
				if f.checks != 1 || f.posts != 0 || f.git(f.head, "rev-parse", dockerUpdateBranch) != f.concurrentCommit {
					t.Fatal("already merged update should not create a PR or push")
				}
				return
			}
			f.assertUpdated(1)
			f.git(f.head, "merge-base", "--is-ancestor", f.baseCommit, dockerUpdateBranch)
			for path, want := range tt.preserved {
				f.assertFile(path, want)
			}
			if len(tt.preserved) != 0 {
				f.git(f.head, "merge-base", "--is-ancestor", f.concurrentCommit, dockerUpdateBranch)
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
