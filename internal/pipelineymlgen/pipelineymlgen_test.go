// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package pipelineymlgen

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/microsoft/go-infra/goldentest"
)

func TestConfigParsing(t *testing.T) {
	e := &EvalState{File: "test.yml"}

	text := `
pipelineymlgen:
  output: self
---
hello: world
`
	docs, err := readYAMLFileDocsFromReader(strings.NewReader(text))
	if err != nil {
		t.Fatalf("Failed to read YAML: %v", err)
	}

	cd, content, err := e.EvalFileConfig(docs)
	if err != nil {
		t.Fatalf("EvalFileConfig failed: %v", err)
	}
	if cd == nil || cd.Config == nil {
		t.Errorf("Expected config, got nil")
	} else if cd.Config.Output == nil {
		t.Errorf("Expected output config, got nil")
	}
	if content == nil {
		t.Errorf("Expected content, got nil")
	} else {
		t.Logf("Content: %+v", content)
	}
}

func TestCheckEOLHandling(t *testing.T) {
	// Test combinations of \n and \r\n line endings.
	for _, sourceLE := range []string{"\n", "\r\n"} {
		for _, destLE := range []string{"\n", "\r\n"} {
			t.Run(fmt.Sprintf("from_%q_to_%q", sourceLE, destLE), func(t *testing.T) {
				// Set up a test dir with a trivial .gen.yml file that exercises
				// code paths that are sensitive to EOL:
				testDir := t.TempDir()
				minimalGitRootInit(t, testDir)

				contentLines := []string{
					"first: 1",
					// Comment handling may be fragile in the YML parser itself.
					// If a CRLF makes it in, it may be treated as LFLF, which
					// breaks reproducibility.
					"# This is number two.",
					"second: 2",
					"",
				}

				genFilePath := filepath.Join(testDir, "test.gen.yml")
				genFileContent := strings.Join(
					append(
						[]string{
							"pipelineymlgen:",
							"  output: self",
							"---",
						},
						contentLines...),
					sourceLE,
				)
				if err := os.WriteFile(genFilePath, []byte(genFileContent), 0o644); err != nil {
					t.Fatalf("Failed to write gen file: %v", err)
				}

				destFilePath := strings.ReplaceAll(genFilePath, ".gen.yml", ".yml")
				expectedDestContent := strings.ReplaceAll(codeGenHeader("test.gen.yml"), "\n", destLE) +
					destLE +
					strings.Join(contentLines, destLE)
				if err := os.WriteFile(destFilePath, []byte(expectedDestContent), 0o644); err != nil {
					t.Fatalf("Failed to write destination file: %v", err)
				}

				if err := Run(
					&CmdFlags{
						Verbose: true,
						Check:   true,
					},
					genFilePath,
				); err != nil {
					t.Errorf("Run failed: %v", err)
					// If we failed, run again without Check to see what it would write.
					if err := Run(
						&CmdFlags{
							Verbose: true,
						},
						genFilePath,
					); err != nil {
						t.Errorf("Run without Check also failed: %v", err)
					}
					c, err := os.ReadFile(destFilePath)
					if err != nil {
						t.Fatalf("Failed to read dest file after failure: %v", err)
					}
					t.Logf("Dest file content after failure:\n%q", string(c))
				}
			})
		}
	}
}

// TestEndToEnd makes sure the testdata/TestEndToEnd files round-trip. Note that
// it would also succeed if pipelineymlgen stops doing anything at all: other
// tests are also necessary.
func TestEndToEnd(t *testing.T) {
	testDataDir := t.TempDir()
	minimalGitRootInit(t, testDataDir)

	copyTestData(t, filepath.Join("testdata", t.Name()), testDataDir)

	if err := Run(
		&CmdFlags{
			Verbose:   true,
			Recursive: true,
		},
		testDataDir,
	); err != nil {
		t.Fatalf("Run failed: %v", err)
	}

	// Ensure all generations were reproducible by checking that nothing changed
	// vs. what's in the checked-in testdata dir.
	if err := filepath.WalkDir(testDataDir, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if filepath.Base(path) == ".git" {
			return filepath.SkipDir
		}
		if d.IsDir() {
			return nil
		}

		relPath, err := filepath.Rel(testDataDir, path)
		if err != nil {
			t.Fatalf("Failed to get relative path: %v", err)
		}

		genContent, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("Failed to read generated file %s: %v", path, err)
		}
		goldentest.Check(t, relPath, string(genContent))
		return nil
	}); err != nil {
		t.Fatalf("Failed to walk testdata dir: %v", err)
	}
}

func TestIndividualFiles(t *testing.T) {
	files, err := filepath.Glob(filepath.Join("testdata", t.Name(), "*.gen.yml"))
	if err != nil {
		t.Fatalf("Failed to glob YAML files: %v", err)
	}

	for _, file := range files {
		t.Run(filepath.Base(file), func(t *testing.T) {
			docs, err := readYAMLFileDocs(file)
			if err != nil {
				t.Fatalf("Failed to read YAML file %s: %v", file, err)
			}
			e := &EvalState{File: file}
			n, err := e.evalFileWithConfig(nil, docs[0])
			if err != nil {
				t.Fatalf("Failed to evaluate %s: %v", file, err)
			}
			var out strings.Builder
			if err := writeYAMLDoc(&out, n); err != nil {
				t.Fatalf("Failed to write YAML for %s: %v", file, err)
			}
			goldentest.CheckFullPath(
				t,
				strings.ReplaceAll(file, ".gen.yml", ".golden.yml"),
				out.String())
		})
	}
}

func TestIndividualFileErrors(t *testing.T) {
	files, err := filepath.Glob(filepath.Join("testdata", t.Name(), "*.gen.yml"))
	if err != nil {
		t.Fatalf("Failed to glob YAML files: %v", err)
	}

	for _, file := range files {
		t.Run(filepath.Base(file), func(t *testing.T) {
			docs, err := readYAMLFileDocs(file)
			if err != nil {
				t.Fatalf("Failed to read YAML file %s: %v", file, err)
			}
			e := &EvalState{File: file}
			n, err := e.evalFileWithConfig(nil, docs[0])
			if err != nil {
				goldentest.CheckFullPath(
					t,
					strings.ReplaceAll(file, ".gen.yml", ".golden.txt"),
					err.Error()+"\n")
			} else {
				t.Errorf("Expected failure, but got success for %s: %+v", file, n)
			}
		})
	}
}

// minimalGitRootInit creates a minimal .git structure in dir to make
// "git rev-parse --show-toplevel" work.
func minimalGitRootInit(t *testing.T, dir string) {
	t.Helper()

	gitDir := filepath.Join(dir, ".git")
	if err := errors.Join(
		os.MkdirAll(filepath.Join(gitDir, "refs"), 0o755),
		os.MkdirAll(filepath.Join(gitDir, "objects"), 0o755),
		os.WriteFile(filepath.Join(gitDir, "HEAD"), []byte("ref: refs/heads/main\n"), 0o644),
	); err != nil {
		t.Fatalf("Failed to create minimal git repo in %s: %v", dir, err)
	}
}

// copyTestData recursively copies testdata to a temporary directory
func copyTestData(t *testing.T, srcDir, destDir string) {
	t.Helper()

	err := filepath.Walk(srcDir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}

		relPath, err := filepath.Rel(srcDir, path)
		if err != nil {
			return err
		}

		destPath := filepath.Join(destDir, relPath)

		if info.IsDir() {
			return os.MkdirAll(destPath, info.Mode())
		}

		srcFile, err := os.ReadFile(path)
		if err != nil {
			return err
		}

		return os.WriteFile(destPath, srcFile, info.Mode())
	})
	if err != nil {
		t.Fatalf("Failed to copy test data: %v", err)
	}
}
