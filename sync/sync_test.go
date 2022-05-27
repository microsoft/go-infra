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
	trueBool := true
	none := "none"
	f := &Flags{
		DryRun:        &trueBool,
		GitAuthString: &none,
	}

	tests := []struct {
		name                                               string
		initialVersion, initialRevision, initialSubVersion string
		version, revision                                  string
		wantVersionContent, wantRevisionContent            string
	}{
		{
			"matching version",
			"", "", "go1.18",
			"go1.18", "",
			"", "",
		},
		{
			"create rev2 version (boring branch)",
			"", "", "",
			"go1.18", "2",
			"go1.18", "2",
		},
		{
			"update rev1 version (boring branch)",
			"go1.18", "2", "",
			"go1.18.2", "1",
			"go1.18.2", "",
		},
		{
			"remove version",
			"go1.18.2", "", "go1.18.3",
			"go1.18.3", "",
			"", "",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
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

			c := &ConfigEntry{
				Upstream: upstream,
				Target:   target,
				BranchMap: map[string]string{
					"main": "microsoft/main",
				},
				SubmoduleTarget:                "go",
				GoVersionFileContent:           tt.version,
				GoMicrosoftRevisionFileContent: tt.revision,
			}

			_, err := MakeBranchPRs(f, workDir, c)
			if err != nil {
				t.Fatal(err)
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
	if err := runGit(dir, "submodule", "add", upstream, "go"); err != nil {
		return err
	}
	if err := runGit(dir, "commit", "-m", "Add submodule"); err != nil {
		return err
	}
	return nil
}

func addMockFile(dir, relativePath, content string) error {
	if err := os.WriteFile(filepath.Join(dir, relativePath), []byte(content), 0666); err != nil {
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
