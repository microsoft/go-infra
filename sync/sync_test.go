// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package sync

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/microsoft/go-infra/goldentest"
)

func Test_createCommitMessageSnippet(t *testing.T) {
	maxUpstreamCommitMessageInSnippet = 20
	snippetCutoffIndicator = "[...]"

	tests := []struct {
		name    string
		message string
		want    string
	}{
		// Test snippet truncation.
		{"short", "Test message", "Test message"},
		{
			"near cutoff",
			"12345678901234567890",
			"12345678901234567890",
		},
		{
			"one past cutoff",
			"12345678901234567890-",
			"1234567890123456[...]",
		},
		{
			"three past cutoff",
			"12345678901234567890---",
			"1234567890123456[...]",
		},
		{"long", strings.Repeat("words ", 80), "words words word[...]"},

		// Test that snippet creation only takes the first line.
		{"newline", "PR Title\nContent", "PR Title"},
		{"newline Windows", "PR Title\r\nContent", "PR Title"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := createCommitMessageSnippet(tt.message); got != tt.want {
				t.Errorf("createCommitMessageSnippet() got = %v, want %v", got, tt.want)
			}
		})
	}
}

func Test_MakeBranchPRs_VersionUpdate(t *testing.T) {
	makeFlags := func(createBranches bool) *Flags {
		trueBool := true
		none := "none"
		var emptyString string
		return &Flags{
			DryRun:          &trueBool,
			GitAuthString:   &none,
			InitialCloneDir: &emptyString,
			CreateBranches:  &createBranches,
		}
	}

	tests := []struct {
		name                                               string
		initialVersion, initialRevision, initialSubVersion string
		targetBranchExists                                 bool
		flags                                              *Flags
		version, revision                                  string
		wantVersionContent, wantRevisionContent            string
	}{
		{
			"matching version",
			"", "", "go1.18",
			true,
			makeFlags(false),
			"go1.18", "",
			"", "",
		},
		{
			"create rev2 version (boring branch)",
			"", "", "",
			true,
			makeFlags(false),
			"go1.18", "2",
			"go1.18", "2",
		},
		{
			"update rev1 version (boring branch)",
			"go1.18", "2", "",
			true,
			makeFlags(false),
			"go1.18.2", "1",
			"go1.18.2", "1",
		},
		{
			"update rev1 version (boring branch) with create-branches enabled",
			"go1.18", "2", "",
			true,
			// This test case should not create any branches, but it confirms that enabling this
			// flag doesn't cause errors in ordinary cases.
			makeFlags(true),
			"go1.18.2", "1",
			"go1.18.2", "1",
		},
		{
			"remove version",
			"go1.18.2", "", "go1.18.3",
			true,
			makeFlags(false),
			"go1.18.3", "",
			"", "",
		},
		{
			"no target branch",
			"go1.18", "2", "",
			false,
			makeFlags(true),
			"go1.18.2", "1",
			"go1.18.2", "",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			d := t.TempDir()
			// Make sure the path ends in "/<owner>/<repo>" so this part of our mock repository
			// paths can be parsed as if they're GitHub repository URLs.
			target := filepath.Join(d, "target") + "/microsoft/go"
			upstream := filepath.Join(d, "upstream") + "/golang/go"

			workDir := filepath.Join(d, "work")

			// Set up upstream, simulated golang/go.
			if err := setupMockRepo(upstream, "main"); err != nil {
				t.Fatal(err)
			}
			if tt.initialSubVersion != "" {
				if err := addMockFile(upstream, "VERSION", tt.initialSubVersion); err != nil {
					t.Fatal(err)
				}
			}

			// Set up target, simulated microsoft/go.
			if err := setupMockRepo(target, "microsoft/main"); err != nil {
				t.Fatal(err)
			}
			if err := addMockSubmodule(target, upstream); err != nil {
				t.Fatal(err)
			}
			if tt.initialVersion != "" {
				if err := addMockFile(target, "VERSION", tt.initialVersion); err != nil {
					t.Fatal(err)
				}
			}
			if tt.initialRevision != "" {
				if err := addMockFile(target, "MICROSOFT_REVISION", tt.initialRevision); err != nil {
					t.Fatal(err)
				}
			}

			// Simulate an upstream change that needs to be synced.
			if err := addMockFile(upstream, "release-notes.md", "Bug has been fixed"); err != nil {
				t.Fatal(err)
			}

			syncBranch := "main"
			if !tt.targetBranchExists {
				syncBranch = "release-branch.go1.18"
			}
			c := &ConfigEntry{
				Upstream: upstream,
				Target:   target,
				BranchMap: map[string]string{
					"main":            "microsoft/main",
					"release-branch*": "microsoft/release-branch?",
				},
				AutoSyncBranches: []string{
					syncBranch,
				},
				MainBranch:                     "microsoft/main",
				SubmoduleTarget:                "go",
				GoVersionFileContent:           tt.version,
				GoMicrosoftRevisionFileContent: tt.revision,
			}

			_, err := MakeBranchPRs(tt.flags, workDir, c)
			if err != nil {
				if errors.Is(err, errWouldCreateBranchButCurrentlyDryRun) {
					if !tt.targetBranchExists {
						// The test runs in dry run mode, so this error should happen.
						return
					}
				}
				t.Fatal(err)
			}
			if !tt.targetBranchExists {
				t.Fatal("MakeBranchPRs is expected to create a new branch, but didn't.")
			}

			wVersion := filepath.Join(workDir, "VERSION")
			if tt.wantVersionContent == "" {
				ensureMissing(t, wVersion)
			} else {
				ensureFileContent(t, wVersion, tt.wantVersionContent)
			}

			wRevision := filepath.Join(workDir, "MICROSOFT_REVISION")
			if tt.wantRevisionContent == "" {
				ensureMissing(t, wRevision)
			} else {
				ensureFileContent(t, wRevision, tt.wantRevisionContent)
			}
		})
	}
}

func ensureMissing(t *testing.T, path string) {
	_, err := os.Stat(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return
		}
		t.Fatalf("unknown error while ensuring file %#q is missing: %v", path, err)
	}
	t.Errorf("file exists, but shouldn't: %v", path)
}

func ensureFileContent(t *testing.T, path, want string) {
	s, err := readFileString(path)
	if err != nil {
		t.Fatal(err)
	}
	if s != want {
		t.Errorf("content wanted: %#q, got: %#q in file %#q", want, s, path)
	}
}

func readFileString(path string) (string, error) {
	bytes, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	return string(bytes), nil
}

func setupMockRepo(dir, branch string) error {
	if err := os.MkdirAll(dir, os.ModePerm); err != nil {
		return err
	}
	if err := runGit(dir, "init"); err != nil {
		return err
	}
	if err := runGit(dir, "checkout", "-b", branch); err != nil {
		return err
	}
	// Initial commit, to make sure the branch exists.
	if err := addMockFile(dir, "README.md", "Hello"); err != nil {
		return err
	}
	return nil
}

func addMockSubmodule(dir, upstream string) error {
	// "protocol.file.allow=always" lets the submodule command clone from a local directory. It's
	// necessary as of Git 2.38.1, where the default was changed to "user" in response to
	// CVE-2022-39253. It isn't a concern here where all repos involved are trusted. For more
	// information, see:
	// https://github.blog/2022-10-18-git-security-vulnerabilities-announced/#cve-2022-39253
	// https://bugs.launchpad.net/ubuntu/+source/git/+bug/1993586
	// https://git-scm.com/docs/git-config#Documentation/git-config.txt-protocolallow
	if err := runGit(dir, "-c", "protocol.file.allow=always", "submodule", "add", upstream, "go"); err != nil {
		return err
	}
	if err := runGit(dir, "commit", "-m", "Add submodule"); err != nil {
		return err
	}
	return nil
}

func addMockFile(dir, relativePath, content string) error {
	if err := os.WriteFile(filepath.Join(dir, relativePath), []byte(content), 0o666); err != nil {
		return err
	}
	if err := runGit(dir, "add", "."); err != nil {
		return err
	}
	if err := runGit(dir, "commit", "-m", "Add "+relativePath); err != nil {
		return err
	}
	return nil
}

func runGit(dir string, args ...string) error {
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	return run(cmd)
}

func Test_formatUpstreamCommitDetails(t *testing.T) {
	tests := []struct {
		name           string
		ownerSlashRepo string
		oldCommit      string
		newCommit      string
		commitLog      string
		logFailed      bool
		wantContains   []string
		wantNotContain []string
	}{
		{
			name:           "multiple commits",
			ownerSlashRepo: "golang/go",
			oldCommit:      "abc1234",
			newCommit:      "def5678",
			commitLog:      "def5678 runtime: reduce allocations\nabc1235 cmd/go: fix module loading",
			wantContains: []string{
				"<details><summary>Upstream commits included in this update</summary>",
				"- [`def5678`](https://github.com/golang/go/commit/def5678) `runtime: reduce allocations`",
				"- [`abc1235`](https://github.com/golang/go/commit/abc1235) `cmd/go: fix module loading`",
				"[View full diff on GitHub](https://github.com/golang/go/compare/abc1234...def5678)",
				"</details>",
			},
		},
		{
			name:           "single commit",
			ownerSlashRepo: "golang/go",
			oldCommit:      "aaa0000",
			newCommit:      "bbb1111",
			commitLog:      "bbb1111 net/http: fix redirect handling",
			wantContains: []string{
				"- [`bbb1111`](https://github.com/golang/go/commit/bbb1111) `net/http: fix redirect handling`",
				"https://github.com/golang/go/compare/aaa0000...bbb1111",
			},
		},
		{
			name:           "log failed",
			ownerSlashRepo: "golang/go",
			oldCommit:      "aaa0000",
			newCommit:      "bbb1111",
			commitLog:      "",
			logFailed:      true,
			wantContains: []string{
				"Could not retrieve commit list.",
				"https://github.com/golang/go/compare/aaa0000...bbb1111",
			},
			wantNotContain: []string{
				"- [`",
				"No commits in range.",
			},
		},
		{
			name:           "empty range",
			ownerSlashRepo: "golang/go",
			oldCommit:      "aaa0000",
			newCommit:      "bbb1111",
			commitLog:      "",
			logFailed:      false,
			wantContains: []string{
				"No commits in range.",
				"https://github.com/golang/go/compare/aaa0000...bbb1111",
			},
			wantNotContain: []string{
				"- [`",
				"Could not retrieve commit list.",
			},
		},
		{
			name:           "commit line without space",
			ownerSlashRepo: "golang/go",
			oldCommit:      "aaa0000",
			newCommit:      "bbb1111",
			commitLog:      "bbb1111",
			wantContains: []string{
				"- `bbb1111`",
			},
		},
		{
			name:           "fix keyword",
			ownerSlashRepo: "golang/go",
			oldCommit:      "aaa0000",
			newCommit:      "bbb1111",
			commitLog:      "61c83b6407 sinit.c: recursion in sinit fixes #1617",
		},
		{
			name:           "auto-linked issue references",
			ownerSlashRepo: "golang/go",
			oldCommit:      "aaa0000",
			newCommit:      "bbb1111",
			commitLog: "4d95fe6653 test: add regress test for #53619\n" +
				"4d95fe6653 test: add regress test for golang/go#53619\n" +
				"4d95fe6653 test: add regress test for https://github.com/golang/go/issues/53619",
		},
		{
			name:           "markdown syntax in commit message",
			ownerSlashRepo: "golang/go",
			oldCommit:      "aaa0000",
			newCommit:      "bbb1111",
			commitLog:      "4d95fe6653 doc: mention `go test` fixes #53619 and <details> for @ghost",
		},
		{
			name:           "handful",
			ownerSlashRepo: "golang/go",
			oldCommit:      "abdc5da461185ab87c1240384e9a66339219f766",
			newCommit:      "39b11f4b14ee3ecce33588a48f9190bc49363e75",
			commitLog: `39b11f4b14 [dev.simd] simd: add ARM64 NEON shift intrinsics
e4283592e5 fmt: give advice on wrapper functions
098d688071 [dev.simd] simdgen: add argsMatchRule for broadcast-to-VMOVI folding
1bcea1df64 cmd/{vet,fix}: use new constants from /x/tools/go/analysis/suite
ae1c21739d [dev.simd] simd: add ARM64 NEON Broadcast and String helpers
399bc412ae [dev.simd] simd: add ARM64 NEON support for partial slice operations
60f0ced65b internal/testenv: make MustHaveSource detect missing source
e0a8616941 math/rand/v2: add method Rand.N
8621461b26 cmd: update vendored x/arch
0db3804845 archive/zip: turn off large zip test on 32-bit archs
abdc5da461 simd/archsimd/_gen: annotate text/template usage`,
		},
		{
			name:           "backticks",
			ownerSlashRepo: "golang/go",
			oldCommit:      "aaa0000",
			newCommit:      "bbb1111",
			commitLog: "e5489a34ca crypto/x509: add missing `be` to comment about serial number positivity\n" +
				"73652af80d cmd/compile: use `else if` for mutually exclusive `if` statements\n" +
				"54af9a3ba5 runtime: reintroduce ``dead'' space during GC scan",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := formatUpstreamCommitDetails(tt.ownerSlashRepo, tt.oldCommit, tt.newCommit, tt.commitLog, tt.logFailed)
			for _, want := range tt.wantContains {
				if !strings.Contains(got, want) {
					t.Errorf("formatUpstreamCommitDetails() missing expected content %q\ngot:\n%s", want, got)
				}
			}
			for _, notWant := range tt.wantNotContain {
				if strings.Contains(got, notWant) {
					t.Errorf("formatUpstreamCommitDetails() contains unexpected content %q\ngot:\n%s", notWant, got)
				}
			}
			// Don't include the <details> block and repeated \n for the golden
			// output so it's easier to preview.
			gotSimple := strings.ReplaceAll(got, "\n\n<details><summary>Upstream commits included in this update</summary>\n\n", "")
			gotSimple = strings.ReplaceAll(gotSimple, "\n</details>", "")
			goldentest.Check(t, "*.PRDescription.md", gotSimple)
		})
	}
}
