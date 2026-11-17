// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package dockerfilegen

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func TestRunTemplateSource(t *testing.T) {
	for _, tt := range []struct {
		name         string
		submoduleDir string
		fork         bool
	}{
		{name: "default submodule", submoduleDir: "go"},
		{name: "renamed submodule", submoduleDir: "golang"},
		{name: "nested submodule", submoduleDir: filepath.Join("upstream", "golang")},
		{name: "legacy fork", submoduleDir: "go", fork: true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			repoRoot := t.TempDir()
			sourceDir := repoRoot
			if !tt.fork {
				sourceDir = filepath.Join(repoRoot, tt.submoduleDir)
			}
			destinationDir := filepath.Join(repoRoot, "src", "microsoft")
			for _, dir := range []string{sourceDir, destinationDir} {
				if err := os.MkdirAll(dir, 0o755); err != nil {
					t.Fatal(err)
				}
			}
			const template = "Dockerfile-linux.template"
			const content = "FROM example\n"
			if err := os.WriteFile(filepath.Join(sourceDir, template), []byte(content), 0o600); err != nil {
				t.Fatal(err)
			}

			// Leave apply-templates.sh absent to test source selection without requiring a shell.
			err := Run(repoRoot, tt.submoduleDir, false, true)
			if !errors.Is(err, os.ErrNotExist) && !errors.Is(err, exec.ErrNotFound) {
				t.Fatalf("Run error = %v, want missing generation script", err)
			}
			got, err := os.ReadFile(filepath.Join(destinationDir, template))
			if err != nil {
				t.Fatal(err)
			}
			if string(got) != content {
				t.Errorf("copied template = %q, want %q", got, content)
			}
		})
	}
}
