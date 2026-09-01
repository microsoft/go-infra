// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestParseSubmoduleReferences(t *testing.T) {
	references, err := parseSubmoduleReferences(
		" https://github.com/golang/go , /src/go,https://github.com/docker-library/golang,/src/golang ",
	)
	if err != nil {
		t.Fatal(err)
	}

	if got, want := references["https://github.com/golang/go"], "/src/go"; got != want {
		t.Errorf("golang/go reference: got %q, want %q", got, want)
	}
	if got, want := references["https://github.com/docker-library/golang"], "/src/golang"; got != want {
		t.Errorf("docker-library/golang reference: got %q, want %q", got, want)
	}
}

func TestParseSubmoduleReferencesInvalid(t *testing.T) {
	for _, value := range []string{
		"https://github.com/golang/go",
		"https://github.com/golang/go,",
		",/src/go",
	} {
		t.Run(value, func(t *testing.T) {
			if _, err := parseSubmoduleReferences(value); err == nil {
				t.Error("parseSubmoduleReferences unexpectedly succeeded")
			}
		})
	}
}

func TestConfiguredSubmoduleReference(t *testing.T) {
	rootDir := t.TempDir()
	gitmodules := `[submodule "name-different-from-path"]
	path = nested/go
	url = https://github.com/golang/go
`
	if err := os.WriteFile(filepath.Join(rootDir, ".gitmodules"), []byte(gitmodules), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv(
		submoduleReferencesEnv,
		"https://github.com/other/repo,/src/other, https://github.com/golang/go, /src/go",
	)

	got, err := configuredSubmoduleReference(rootDir, filepath.Join(rootDir, "nested", "go"))
	if err != nil {
		t.Fatal(err)
	}
	if want := "/src/go"; got != want {
		t.Errorf("configuredSubmoduleReference: got %q, want %q", got, want)
	}
}

func TestConfiguredSubmoduleReferenceNoMatch(t *testing.T) {
	rootDir := t.TempDir()
	gitmodules := `[submodule "go"]
	path = go
	url = https://github.com/golang/go
`
	if err := os.WriteFile(filepath.Join(rootDir, ".gitmodules"), []byte(gitmodules), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv(submoduleReferencesEnv, "https://github.com/other/repo,/src/other")

	got, err := configuredSubmoduleReference(rootDir, filepath.Join(rootDir, "go"))
	if err != nil {
		t.Fatal(err)
	}
	if got != "" {
		t.Errorf("configuredSubmoduleReference: got %q, want no reference", got)
	}
}
