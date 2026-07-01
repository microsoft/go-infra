// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestReadFileContent(t *testing.T) {
	dir := t.TempDir()

	// A missing file is expected and not an error.
	if got, err := readFileContent(filepath.Join(dir, "absent.txt")); err != nil || got != "" {
		t.Errorf("absent file: got (%q, %v), want (\"\", nil)", got, err)
	}

	// An existing file is read and trimmed.
	p := filepath.Join(dir, "f.txt")
	if err := os.WriteFile(p, []byte("  hello\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got, err := readFileContent(p); err != nil || got != "hello" {
		t.Errorf("existing file: got (%q, %v), want (\"hello\", nil)", got, err)
	}

	// A genuine read error (here, reading a directory) must be reported.
	if _, err := readFileContent(dir); err == nil {
		t.Error("expected an error reading a directory, got nil")
	}
}

func TestLoadJobURLs(t *testing.T) {
	dir := t.TempDir()

	// Empty path and missing file both yield an empty map without error.
	if m, err := loadJobURLs(""); err != nil || len(m) != 0 {
		t.Errorf("empty path: got (%v, %v)", m, err)
	}
	if m, err := loadJobURLs(filepath.Join(dir, "absent.tsv")); err != nil || len(m) != 0 {
		t.Errorf("absent file: got (%v, %v)", m, err)
	}

	// A well-formed TSV is parsed.
	p := filepath.Join(dir, "urls.tsv")
	if err := os.WriteFile(p, []byte("label-a\thttps://x/1\nlabel-b\thttps://x/2\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	m, err := loadJobURLs(p)
	if err != nil {
		t.Fatal(err)
	}
	if m["label-a"] != "https://x/1" || m["label-b"] != "https://x/2" {
		t.Errorf("unexpected map: %v", m)
	}

	// A genuine read error (directory) must be reported.
	if _, err := loadJobURLs(dir); err == nil {
		t.Error("expected an error reading a directory, got nil")
	}
}
