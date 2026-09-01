// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package patch

import (
	"bufio"
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/microsoft/go-infra/gitcmd"
)

func TestTryThreeWayRebase(t *testing.T) {
	setThreeWayTestGitEnv(t)

	const (
		original = "aaa\nbbb\nccc\nddd\neee\nfff\nggg\nhhh\niii\njjj\n"
		patched  = "aaa\nbbb\nccc\nddd\nEEE\nfff\nggg\nhhh\niii\njjj\n"
		diverged = "AAA\nBBB\nccc\nddd\neee\nfff\nggg\nHHH\nIII\nJJJ\n"
		merged   = "AAA\nBBB\nccc\nddd\nEEE\nfff\nggg\nHHH\nIII\nJJJ\n"
		conflict = "aaa\nbbb\nccc\nddd\nUPSTREAM\nfff\nggg\nhhh\niii\njjj\n"
	)

	tests := []struct {
		name             string
		newBaseContent   string
		relativeRoot     bool
		wantPatchChanged bool
		wantApplied      string
	}{
		{
			name:           "clean apply needs no rebase",
			newBaseContent: original,
			wantApplied:    patched,
		},
		{
			name:             "three-way apply rebases patch",
			newBaseContent:   diverged,
			relativeRoot:     true,
			wantPatchChanged: true,
			wantApplied:      merged,
		},
		{
			name:           "three-way conflict leaves patch unchanged",
			newBaseContent: conflict,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			repoDir, newBase, patchPath, originalPatch := setupThreeWayRebaseRepo(
				t,
				original,
				patched,
				tt.newBaseContent,
			)

			rootDir := repoDir
			if tt.relativeRoot {
				workingDir, err := os.Getwd()
				if err != nil {
					t.Fatal(err)
				}
				rootDir, err = filepath.Rel(workingDir, repoDir)
				if err != nil {
					t.Skipf("cannot create a relative temp path from %q: %v", workingDir, err)
				}
			}

			updatedFiles, err := TryThreeWayRebase(rootDir, "go", newBase)
			if err != nil {
				t.Fatal(err)
			}

			updatedPatch, err := os.ReadFile(patchPath)
			if err != nil {
				t.Fatal(err)
			}
			patchChanged := string(updatedPatch) != string(originalPatch)
			if patchChanged != tt.wantPatchChanged {
				t.Errorf("patch changed = %v, want %v", patchChanged, tt.wantPatchChanged)
			}

			if tt.wantPatchChanged {
				wantUpdatedPath := filepath.Join("patches", filepath.Base(patchPath))
				if len(updatedFiles) != 1 || updatedFiles[0] != wantUpdatedPath {
					t.Errorf("updatedFiles = %v, want [%v]", updatedFiles, wantUpdatedPath)
				}
				originalParsed, err := Read(bytes.NewReader(originalPatch))
				if err != nil {
					t.Fatal(err)
				}
				updatedParsed, err := Read(bytes.NewReader(updatedPatch))
				if err != nil {
					t.Fatal(err)
				}
				if originalParsed.Header != updatedParsed.Header {
					t.Errorf("regenerated patch header changed:\n%+v\nwant:\n%+v", updatedParsed.Header, originalParsed.Header)
				}
			} else if len(updatedFiles) != 0 {
				t.Errorf("updatedFiles = %v, want none", updatedFiles)
			}

			if tt.wantApplied != "" {
				verifyPatchApplies(t, repoDir, newBase, patchPath, tt.wantApplied)
			}
		})
	}

	t.Run("later clean patch remains unchanged", func(t *testing.T) {
		repoDir, newBase, firstPatchPath, firstPatch := setupThreeWayRebaseRepo(t, original, patched, diverged)
		secondPatchPath, secondPatch := addPatchAfterFirst(t, repoDir, newBase, firstPatchPath, "second.txt", "second patch\n")

		updatedFiles, err := TryThreeWayRebase(repoDir, "go", newBase)
		if err != nil {
			t.Fatal(err)
		}
		wantUpdatedPath := filepath.Join("patches", filepath.Base(firstPatchPath))
		if len(updatedFiles) != 1 || updatedFiles[0] != wantUpdatedPath {
			t.Errorf("updatedFiles = %v, want [%v]", updatedFiles, wantUpdatedPath)
		}
		ensureThreeWayTestFileChanged(t, firstPatchPath, firstPatch, true)
		ensureThreeWayTestFileChanged(t, secondPatchPath, secondPatch, false)
		verifyPatchSeriesApplies(t, repoDir, newBase, firstPatchPath, secondPatchPath)
	})

	t.Run("dependent patches both rebase", func(t *testing.T) {
		longOriginal := "01\n02\n03\n04\n05\n06\n07\n08\n09\n10\n11\n12\n13\n14\n15\n16\n17\n18\n19\n20\n"
		longPatched := strings.Replace(longOriginal, "10\n", "PATCH1\n", 1)
		longDiverged := strings.Replace(longOriginal, "06\n07\n", "UPSTREAM6\nUPSTREAM7\n", 1)
		repoDir, newBase, firstPatchPath, firstPatch := setupThreeWayRebaseRepo(t, longOriginal, longPatched, longDiverged)
		secondPatchContent := strings.Replace(longPatched, "03\n", "PATCH2\n", 1)
		secondPatchPath, secondPatch := addPatchAfterFirst(t, repoDir, newBase, firstPatchPath, "file.txt", secondPatchContent)

		updatedFiles, err := TryThreeWayRebase(repoDir, "go", newBase)
		if err != nil {
			t.Fatal(err)
		}
		wantUpdatedFiles := []string{
			filepath.Join("patches", filepath.Base(firstPatchPath)),
			filepath.Join("patches", filepath.Base(secondPatchPath)),
		}
		if !slices.Equal(updatedFiles, wantUpdatedFiles) {
			t.Errorf("updatedFiles = %v, want %v", updatedFiles, wantUpdatedFiles)
		}
		ensureThreeWayTestFileChanged(t, firstPatchPath, firstPatch, true)
		ensureThreeWayTestFileChanged(t, secondPatchPath, secondPatch, true)
		verifyPatchSeriesApplies(t, repoDir, newBase, firstPatchPath, secondPatchPath)
	})

	t.Run("later conflict discards earlier rebase", func(t *testing.T) {
		repoDir, newBase, firstPatchPath, firstPatch := setupThreeWayRebaseRepo(t, original, patched, diverged)
		secondPatchContent := strings.Replace(patched, "jjj\n", "PATCHED\n", 1)
		secondPatchPath, secondPatch := addPatchAfterFirst(t, repoDir, newBase, firstPatchPath, "file.txt", secondPatchContent)

		updatedFiles, err := TryThreeWayRebase(repoDir, "go", newBase)
		if err != nil {
			t.Fatal(err)
		}
		if len(updatedFiles) != 0 {
			t.Errorf("updatedFiles = %v, want none", updatedFiles)
		}
		ensureThreeWayTestFileChanged(t, firstPatchPath, firstPatch, false)
		ensureThreeWayTestFileChanged(t, secondPatchPath, secondPatch, false)
	})

	t.Run("configured patch author is used", func(t *testing.T) {
		const author = "Patch Bot <patch-bot@example.com>"
		repoDir, newBase, patchPath, _ := setupThreeWayRebaseRepo(t, original, patched, diverged)
		writeThreeWayTestFile(t, filepath.Join(repoDir, ConfigFileName), `{"SubmoduleDir":"go","PatchesDir":"patches","ExtractAsAuthor":"`+author+`"}`)

		if _, err := TryThreeWayRebase(repoDir, "go", newBase); err != nil {
			t.Fatal(err)
		}
		p, err := ReadFile(patchPath)
		if err != nil {
			t.Fatal(err)
		}
		if p.FromAuthor != author {
			t.Errorf("regenerated patch author = %q, want %q", p.FromAuthor, author)
		}
	})
}

func TestFormatPatch(t *testing.T) {
	setThreeWayTestGitEnv(t)
	repoDir := t.TempDir()
	runThreeWayTestGit(t, repoDir, "init")
	runThreeWayTestGit(t, repoDir, "commit", "--allow-empty", "-m", "base")
	writeThreeWayTestFile(t, filepath.Join(repoDir, "file.txt"), "content\n")
	runThreeWayTestGit(t, repoDir, "add", "file.txt")
	runThreeWayTestGit(t, repoDir, "commit", "-m", "change")

	outputDir := t.TempDir()
	if _, err := FormatPatch(repoDir, "-o", outputDir, "HEAD^"); err != nil {
		t.Fatal(err)
	}
	paths, err := filepath.Glob(filepath.Join(outputDir, "*.patch"))
	if err != nil {
		t.Fatal(err)
	}
	if len(paths) != 1 {
		t.Fatalf("FormatPatches created %v patches, want 1", len(paths))
	}
	fromFile, err := ReadFile(paths[0])
	if err != nil {
		t.Fatal(err)
	}
	formatted, err := FormatPatch(repoDir, "--stdout", "-1", "HEAD")
	if err != nil {
		t.Fatal(err)
	}
	fromStdout, err := Read(strings.NewReader(formatted))
	if err != nil {
		t.Fatal(err)
	}
	if fromFile.String() != fromStdout.String() {
		t.Error("directory and single-commit formatting produced different patch content")
	}
}

func setThreeWayTestGitEnv(t *testing.T) {
	t.Helper()
	emptyGitConfig := filepath.Join(t.TempDir(), "gitconfig")
	writeThreeWayTestFile(t, emptyGitConfig, "")
	t.Setenv("GIT_CONFIG_GLOBAL", emptyGitConfig)
	t.Setenv("GIT_CONFIG_SYSTEM", emptyGitConfig)
	t.Setenv("GIT_AUTHOR_NAME", "Patch Test")
	t.Setenv("GIT_AUTHOR_EMAIL", "patch-test@example.com")
	t.Setenv("GIT_COMMITTER_NAME", "Patch Test")
	t.Setenv("GIT_COMMITTER_EMAIL", "patch-test@example.com")
}

func TestTryThreeWayRebaseNoPatches(t *testing.T) {
	rootDir := t.TempDir()
	if err := os.Mkdir(filepath.Join(rootDir, "patches"), 0o755); err != nil {
		t.Fatal(err)
	}
	writeThreeWayTestFile(t, filepath.Join(rootDir, ConfigFileName), `{"SubmoduleDir":"go","PatchesDir":"patches"}`)

	updatedFiles, err := TryThreeWayRebase(rootDir, "go", "not-a-commit")
	if err != nil {
		t.Fatal(err)
	}
	if len(updatedFiles) != 0 {
		t.Errorf("TryThreeWayRebase updated files = %v, want none", updatedFiles)
	}
}

func TestTryThreeWayRebaseNoMatchingConfig(t *testing.T) {
	tests := []struct {
		name   string
		config string
	}{
		{name: "no config"},
		{name: "different submodule", config: `{"SubmoduleDir":"other","PatchesDir":"patches"}`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rootDir := t.TempDir()
			if tt.config != "" {
				writeThreeWayTestFile(t, filepath.Join(rootDir, ConfigFileName), tt.config)
			}

			updatedFiles, err := TryThreeWayRebase(rootDir, "go", "not-a-commit")
			if err != nil {
				t.Fatal(err)
			}
			if len(updatedFiles) != 0 {
				t.Errorf("TryThreeWayRebase updated files = %v, want none", updatedFiles)
			}
		})
	}
}

func TestReadRejectsOversizedLine(t *testing.T) {
	if _, err := Read(strings.NewReader(strings.Repeat("x", bufio.MaxScanTokenSize+1))); err == nil {
		t.Fatal("Read succeeded with an oversized line, want scanner error")
	}
}

func setupThreeWayRebaseRepo(t *testing.T, original, patched, newBaseContent string) (repoDir, newBase, patchPath string, originalPatch []byte) {
	t.Helper()

	repoDir = t.TempDir()
	runThreeWayTestGit(t, repoDir, "init")

	filePath := filepath.Join(repoDir, "file.txt")
	writeThreeWayTestFile(t, filePath, original)
	runThreeWayTestGit(t, repoDir, "add", "file.txt")
	runThreeWayTestGit(t, repoDir, "commit", "-m", "initial")
	oldBase := threeWayTestGitOutput(t, repoDir, "rev-parse", "HEAD")

	writeThreeWayTestFile(t, filePath, patched)
	runThreeWayTestGit(t, repoDir, "add", "file.txt")
	runThreeWayTestGit(t, repoDir, "commit", "-m", "modify middle")

	patchDir := filepath.Join(repoDir, "patches")
	if err := os.Mkdir(patchDir, 0o755); err != nil {
		t.Fatal(err)
	}
	runThreeWayTestGit(
		t,
		repoDir,
		"format-patch",
		"--abbrev=14",
		"--signature=",
		"--zero-commit",
		"--no-numbered",
		"-1",
		"-o",
		patchDir,
	)
	patches, err := filepath.Glob(filepath.Join(patchDir, "*.patch"))
	if err != nil {
		t.Fatal(err)
	}
	if len(patches) != 1 {
		t.Fatalf("format-patch created %v patches, want 1", len(patches))
	}
	patchPath = patches[0]
	originalPatch, err = os.ReadFile(patchPath)
	if err != nil {
		t.Fatal(err)
	}

	// The patch commit isn't part of upstream history. Reset it out of every ref so the temporary
	// clone only gets the original upstream blob referenced by the patch, as it would in sync.
	runThreeWayTestGit(t, repoDir, "reset", "--hard", oldBase)
	runThreeWayTestGit(t, repoDir, "checkout", "-b", "new-base")
	if newBaseContent != original {
		writeThreeWayTestFile(t, filePath, newBaseContent)
		runThreeWayTestGit(t, repoDir, "add", "file.txt")
		runThreeWayTestGit(t, repoDir, "commit", "-m", "update upstream")
	}
	newBase = threeWayTestGitOutput(t, repoDir, "rev-parse", "HEAD")
	// Sync keeps the target branch checked out while the new upstream commit is referenced by a
	// separately fetched branch. Make sure the temporary clone carries that commit across.
	runThreeWayTestGit(t, repoDir, "checkout", "-b", "sync-target", oldBase)

	writeThreeWayTestFile(t, filepath.Join(repoDir, ConfigFileName), `{"SubmoduleDir":"go","PatchesDir":"patches"}`)
	return repoDir, newBase, patchPath, originalPatch
}

func addPatchAfterFirst(t *testing.T, repoDir, newBase, firstPatchPath, relativePath, content string) (string, []byte) {
	t.Helper()

	runThreeWayTestGit(t, repoDir, "checkout", "-b", "patch-series", newBase+"^")
	runThreeWayTestGit(t, repoDir, "-c", "am.threeWay=false", "am", firstPatchPath)
	writeThreeWayTestFile(t, filepath.Join(repoDir, relativePath), content)
	runThreeWayTestGit(t, repoDir, "add", relativePath)
	runThreeWayTestGit(t, repoDir, "commit", "-m", "second patch")

	patchDir := filepath.Dir(firstPatchPath)
	runThreeWayTestGit(
		t,
		repoDir,
		"format-patch",
		"--abbrev=14",
		"--signature=",
		"--zero-commit",
		"--no-numbered",
		"--start-number=2",
		"-1",
		"-o",
		patchDir,
	)
	runThreeWayTestGit(t, repoDir, "checkout", "sync-target")
	runThreeWayTestGit(t, repoDir, "branch", "-D", "patch-series")

	patches, err := filepath.Glob(filepath.Join(patchDir, "*.patch"))
	if err != nil {
		t.Fatal(err)
	}
	if len(patches) != 2 {
		t.Fatalf("found %v patches after adding second patch, want 2", len(patches))
	}
	for _, patchPath := range patches {
		if patchPath != firstPatchPath {
			content, err := os.ReadFile(patchPath)
			if err != nil {
				t.Fatal(err)
			}
			return patchPath, content
		}
	}
	t.Fatal("failed to find second patch")
	return "", nil
}

func ensureThreeWayTestFileChanged(t *testing.T, path string, original []byte, wantChanged bool) {
	t.Helper()
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if changed := string(content) != string(original); changed != wantChanged {
		t.Errorf("file %q changed = %v, want %v", path, changed, wantChanged)
	}
}

func verifyPatchApplies(t *testing.T, sourceRepo, baseCommit, patchPath, wantContent string) {
	t.Helper()

	verifyDir, err := gitcmd.NewTempCloneRepo(sourceRepo)
	if err != nil {
		t.Fatal(err)
	}
	defer gitcmd.AttemptDelete(verifyDir)
	if err := gitcmd.Run(verifyDir, "checkout", "--detach", "--quiet", baseCommit); err != nil {
		t.Fatal(err)
	}
	absolutePatchPath, err := filepath.Abs(patchPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := gitcmd.Am(verifyDir, "--quiet", absolutePatchPath); err != nil {
		t.Fatalf("regenerated patch does not apply cleanly: %v", err)
	}
	gotContent, err := os.ReadFile(filepath.Join(verifyDir, "file.txt"))
	if err != nil {
		t.Fatal(err)
	}
	got := strings.ReplaceAll(string(gotContent), "\r\n", "\n")
	if got != wantContent {
		t.Errorf("content after applying patch:\n%q\nwant:\n%q", gotContent, wantContent)
	}
}

func verifyPatchSeriesApplies(t *testing.T, sourceRepo, baseCommit string, patchPaths ...string) {
	t.Helper()

	verifyDir, err := gitcmd.NewTempCloneRepo(sourceRepo)
	if err != nil {
		t.Fatal(err)
	}
	defer gitcmd.AttemptDelete(verifyDir)
	if err := gitcmd.Run(verifyDir, "checkout", "--detach", "--quiet", baseCommit); err != nil {
		t.Fatal(err)
	}
	args := append([]string{"--quiet"}, patchPaths...)
	if err := gitcmd.Am(verifyDir, args...); err != nil {
		t.Fatalf("patch series does not apply cleanly: %v", err)
	}
}

func runThreeWayTestGit(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v failed: %v\n%s", args, err, output)
	}
}

func threeWayTestGitOutput(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v failed: %v\n%s", args, err, output)
	}
	return strings.TrimSpace(string(output))
}

func writeThreeWayTestFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}
