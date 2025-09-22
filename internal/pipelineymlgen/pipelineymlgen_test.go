// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package pipelineymlgen

import (
	"errors"
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
