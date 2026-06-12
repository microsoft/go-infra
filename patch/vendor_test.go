// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package patch

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestFilterVendorContent(t *testing.T) {
	content := "---\n" +
		" src/go.mod                     |  2 +\n" +
		" src/vendor/foo/bar.go          | 10 +\n" +
		" src/cmd/vendor/baz/qux.go     |  5 +\n" +
		" 3 files changed, 17 insertions(+)\n" +
		"\n" +
		"diff --git a/src/go.mod b/src/go.mod\n" +
		"index abc..def 100644\n" +
		"--- a/src/go.mod\n" +
		"+++ b/src/go.mod\n" +
		"@@ -1,3 +1,5 @@\n" +
		" module std\n" +
		"+\n" +
		"+require example.com/foo v1.0.0\n" +
		"diff --git a/src/vendor/foo/bar.go b/src/vendor/foo/bar.go\n" +
		"new file mode 100644\n" +
		"index 000..abc\n" +
		"--- /dev/null\n" +
		"+++ b/src/vendor/foo/bar.go\n" +
		"@@ -0,0 +1,3 @@\n" +
		"+package foo\n" +
		"+\n" +
		"+func Bar() {}\n" +
		"diff --git a/src/cmd/vendor/baz/qux.go b/src/cmd/vendor/baz/qux.go\n" +
		"new file mode 100644\n" +
		"index 000..def\n" +
		"--- /dev/null\n" +
		"+++ b/src/cmd/vendor/baz/qux.go\n" +
		"@@ -0,0 +1,3 @@\n" +
		"+package baz\n" +
		"+\n" +
		"+func Qux() {}\n" +
		"-- \n" +
		"2.45.0\n"

	vendorPaths := VendorPathsFromModDirs([]string{"src", "src/cmd"})
	result := FilterVendorContent(content, vendorPaths)

	// Should keep the go.mod diff and the trailing signature.
	if !strings.Contains(result, "diff --git a/src/go.mod b/src/go.mod") {
		t.Error("filtered content should contain go.mod diff")
	}
	if !strings.Contains(result, "+require example.com/foo v1.0.0") {
		t.Error("filtered content should contain go.mod diff hunk")
	}
	if strings.Contains(result, "src/vendor/foo/bar.go") {
		t.Error("filtered content should not contain vendor diff")
	}
	if strings.Contains(result, "src/cmd/vendor/baz/qux.go") {
		t.Error("filtered content should not contain cmd vendor diff")
	}
	if !strings.Contains(result, "-- \n2.45.0") {
		t.Error("filtered content should contain trailing signature")
	}
	// Diffstat should be removed.
	if strings.Contains(result, "files changed") {
		t.Error("filtered content should not contain diffstat summary")
	}
	// The "---" separator should still be present.
	if !strings.HasPrefix(result, "---\n") {
		t.Error("filtered content should start with ---")
	}
}

func TestFilterVendorContent_NoVendorFiles(t *testing.T) {
	content := "---\n" +
		" src/go.mod | 2 +\n" +
		" 1 file changed, 2 insertions(+)\n" +
		"\n" +
		"diff --git a/src/go.mod b/src/go.mod\n" +
		"index abc..def 100644\n" +
		"--- a/src/go.mod\n" +
		"+++ b/src/go.mod\n" +
		"@@ -1,3 +1,5 @@\n" +
		" module std\n" +
		"+\n" +
		"+require example.com/foo v1.0.0\n" +
		"-- \n" +
		"2.45.0\n"

	vendorPaths := VendorPathsFromModDirs([]string{"src", "src/cmd"})
	result := FilterVendorContent(content, vendorPaths)

	if !strings.Contains(result, "diff --git a/src/go.mod b/src/go.mod") {
		t.Error("filtered content should contain go.mod diff")
	}
	if !strings.Contains(result, "-- \n2.45.0") {
		t.Error("filtered content should contain trailing signature")
	}
}

func TestFilterVendorContent_VendorBetweenNonVendor(t *testing.T) {
	// Vendor diff appears between two non-vendor diffs.
	content := "---\n" +
		"\n" +
		"diff --git a/src/go.mod b/src/go.mod\n" +
		"--- a/src/go.mod\n" +
		"+++ b/src/go.mod\n" +
		"+require foo v1.0\n" +
		"diff --git a/src/vendor/foo/f.go b/src/vendor/foo/f.go\n" +
		"+package foo\n" +
		"diff --git a/src/go.sum b/src/go.sum\n" +
		"--- a/src/go.sum\n" +
		"+++ b/src/go.sum\n" +
		"+foo v1.0 h1:abc=\n" +
		"-- \n" +
		"2.45.0\n"

	vendorPaths := VendorPathsFromModDirs([]string{"src"})
	result := FilterVendorContent(content, vendorPaths)

	if !strings.Contains(result, "diff --git a/src/go.mod b/src/go.mod") {
		t.Error("should keep go.mod diff")
	}
	if !strings.Contains(result, "diff --git a/src/go.sum b/src/go.sum") {
		t.Error("should keep go.sum diff")
	}
	if strings.Contains(result, "src/vendor/foo/f.go") {
		t.Error("should remove vendor diff")
	}
}

func TestVendorPathsFromModDirs(t *testing.T) {
	paths := VendorPathsFromModDirs([]string{"src", "src/cmd"})
	if len(paths) != 2 {
		t.Fatalf("expected 2 paths, got %d", len(paths))
	}
	if paths[0] != "src/vendor/" {
		t.Errorf("expected src/vendor/, got %s", paths[0])
	}
	if paths[1] != "src/cmd/vendor/" {
		t.Errorf("expected src/cmd/vendor/, got %s", paths[1])
	}
}

func TestIsDiffForVendorPath(t *testing.T) {
	vendorPaths := []string{"src/vendor/", "src/cmd/vendor/"}

	tests := []struct {
		line string
		want bool
	}{
		{"diff --git a/src/vendor/foo/bar.go b/src/vendor/foo/bar.go", true},
		{"diff --git a/src/cmd/vendor/baz/qux.go b/src/cmd/vendor/baz/qux.go", true},
		{"diff --git a/src/go.mod b/src/go.mod", false},
		{"diff --git a/src/crypto/deps_ignore.go b/src/crypto/deps_ignore.go", false},
		{"diff --git a/src/go/build/vendor_test.go b/src/go/build/vendor_test.go", false},
	}

	for _, tt := range tests {
		got := isDiffForVendorPath(tt.line, vendorPaths)
		if got != tt.want {
			t.Errorf("isDiffForVendorPath(%q) = %v, want %v", tt.line, got, tt.want)
		}
	}
}

// writeFile is a test helper that calls os.WriteFile and fails the test on error.
func writeFile(t *testing.T, path string, data []byte) {
	t.Helper()
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatal(err)
	}
}

// mkdirAll is a test helper that calls os.MkdirAll and fails the test on error.
func mkdirAll(t *testing.T, path string) {
	t.Helper()
	if err := os.MkdirAll(path, 0o755); err != nil {
		t.Fatal(err)
	}
}

func TestLocalReplaceDirs(t *testing.T) {
	// Set up a temp directory with a main module and a local replace target.
	tmp := t.TempDir()
	mainDir := filepath.Join(tmp, "src")
	replaceDir := filepath.Join(tmp, "cryptobackend")
	mkdirAll(t, mainDir)
	mkdirAll(t, replaceDir)

	writeFile(t, filepath.Join(mainDir, "go.mod"), []byte(
		"module std\n\ngo 1.27\n\n"+
			"require github.com/example/foo v1.0.0\n\n"+
			"replace github.com/example/backend => ../cryptobackend\n"))
	writeFile(t, filepath.Join(replaceDir, "go.mod"), []byte(
		"module github.com/example/backend\n\ngo 1.26\n"))

	dirs, err := localReplaceDirs(mainDir)
	if err != nil {
		t.Fatal(err)
	}
	if len(dirs) != 1 {
		t.Fatalf("expected 1 local replace dir, got %d", len(dirs))
	}
	if !strings.HasSuffix(dirs[0], "cryptobackend") {
		t.Errorf("expected dir ending with cryptobackend, got %s", dirs[0])
	}
}

func TestLocalReplaceDirs_NoLocalReplaces(t *testing.T) {
	tmp := t.TempDir()
	writeFile(t, filepath.Join(tmp, "go.mod"), []byte(
		"module std\n\ngo 1.27\n\n"+
			"require golang.org/x/crypto v0.50.0\n"))

	dirs, err := localReplaceDirs(tmp)
	if err != nil {
		t.Fatal(err)
	}
	if len(dirs) != 0 {
		t.Errorf("expected 0 local replace dirs, got %d", len(dirs))
	}
}

func TestLocalReplaceDirs_SkipsMissingGoMod(t *testing.T) {
	tmp := t.TempDir()
	// Replace target directory exists but has no go.mod.
	mkdirAll(t, filepath.Join(tmp, "nomod"))
	writeFile(t, filepath.Join(tmp, "go.mod"), []byte(
		"module std\n\ngo 1.27\n\n"+
			"replace example.com/foo => ./nomod\n"))

	dirs, err := localReplaceDirs(tmp)
	if err != nil {
		t.Fatal(err)
	}
	if len(dirs) != 0 {
		t.Errorf("expected 0 dirs (no go.mod in target), got %d", len(dirs))
	}
}

func TestCollectLocalGoModDirs(t *testing.T) {
	tmp := t.TempDir()
	mainDir := filepath.Join(tmp, "src")
	replaceDir := filepath.Join(tmp, "backend")
	mkdirAll(t, mainDir)
	mkdirAll(t, replaceDir)

	writeFile(t, filepath.Join(mainDir, "go.mod"), []byte(
		"module std\n\ngo 1.27\n\n"+
			"replace example.com/backend => ../backend\n"))
	writeFile(t, filepath.Join(replaceDir, "go.mod"), []byte(
		"module example.com/backend\n\ngo 1.26\n"))

	dirs, err := collectLocalGoModDirs(mainDir)
	if err != nil {
		t.Fatal(err)
	}
	// Should include mainDir + the replace target.
	if len(dirs) != 2 {
		t.Fatalf("expected 2 dirs, got %d: %v", len(dirs), dirs)
	}
	if dirs[0] != mainDir {
		t.Errorf("first dir should be mainDir, got %s", dirs[0])
	}
}

func TestReadGoDirective(t *testing.T) {
	tmp := t.TempDir()
	writeFile(t, filepath.Join(tmp, "go.mod"), []byte(
		"module example\n\ngo 1.27\n\nrequire foo v1.0\n"))

	version, err := readGoDirective(tmp)
	if err != nil {
		t.Fatal(err)
	}
	if version != "1.27" {
		t.Errorf("expected 1.27, got %s", version)
	}
}

func TestReadGoDirective_Missing(t *testing.T) {
	tmp := t.TempDir()
	writeFile(t, filepath.Join(tmp, "go.mod"), []byte("module example\n"))

	_, err := readGoDirective(tmp)
	if err == nil {
		t.Error("expected error for missing go directive")
	}
}
