// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package importdiff

import (
	"flag"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestCompare(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("GIT_CONFIG_GLOBAL", os.DevNull)
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	git := func(args ...string) string {
		t.Helper()
		out, err := exec.Command("git", append([]string{"-C", dir}, args...)...).CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %s (%v)", args, out, err)
		}
		return strings.TrimSpace(string(out))
	}
	write := func(name, src string) {
		t.Helper()
		name = filepath.Join(dir, name)
		if err := os.MkdirAll(filepath.Dir(name), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(name, []byte(src), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	commit := func() string {
		git("add", ".")
		git("-c", "user.name=Test", "-c", "user.email=test@example.com", "commit", "-qm", "snapshot")
		return git("rev-parse", "HEAD")
	}
	const (
		pkg     = "example/private"
		watched = "package p\nimport _ \"example/private\"\n"
		tagged  = "//go:build special\n\npackage p\nimport alias `example/private/a`\n"
		escaped = "package p\nimport _ \"example/\\x70rivate\"\n"
		other   = "package p\nimport _ \"example/privateonly\"\nconst s = \"example/private\"\n"
	)
	rule := Rule{SourceRoots: []string{"."}, ImportRoots: []string{"unused"}}
	flags := flag.NewFlagSet("importdiff", flag.ContinueOnError)
	rule.BindFlags(flags)
	if err := flags.Parse([]string{
		"-src", "tools", "-src", "src/example", "-src", "src",
		"-import", pkg, "-import", pkg + "/a", "-import", "x.test/lib",
		"-exclude", "src/example/private", "-exclude", "tools/generated",
	}); err != nil {
		t.Fatal(err)
	}
	git("init", "-q")
	write("src/body.go", watched)
	write("src/removed.go", watched)
	write("src/imports.go", watched)
	old := commit()
	write("src/body.go", watched+"// New use through an existing import.\n")
	write("src/imports.go", other)
	git("mv", "src/removed.go", "src/renamed.go")
	write("src/new_windows_test.go", tagged)
	write("src/example/private/hidden.go", watched)
	write("src/example/privateonly/sibling.go", escaped)
	write("src/not-an-import.go", other)
	write("src/second.go", "package p\nimport _ \"x.test/lib/sub\"\n")
	write("src/testdata/bad.go", "not Go")
	write("tools/both.go", "package p\nimport (\n _ \"x.test/lib\"\n _ \"example/private/a\"\n)\n")
	write("tools/generated/hidden.go", watched)
	write("outside.go", watched)
	updated := commit()
	write("src/body.go", "dirty working tree, not Go")

	got, err := Compare(t.Context(), dir, old, updated, rule)
	if err != nil {
		t.Fatal(err)
	}
	want := []Change{
		{"src/body.go", git("rev-parse", old+":src/body.go"), git("rev-parse", updated+":src/body.go"), []string{pkg}, []string{pkg}},
		{"src/example/privateonly/sibling.go", "", git("rev-parse", updated+":src/example/privateonly/sibling.go"), nil, []string{pkg}},
		{"src/imports.go", git("rev-parse", old+":src/imports.go"), git("rev-parse", updated+":src/imports.go"), []string{pkg}, nil},
		{"src/new_windows_test.go", "", git("rev-parse", updated+":src/new_windows_test.go"), nil, []string{pkg + "/a"}},
		{"src/removed.go", git("rev-parse", old+":src/removed.go"), "", []string{pkg}, nil},
		{"src/renamed.go", "", git("rev-parse", updated+":src/renamed.go"), nil, []string{pkg}},
		{"src/second.go", "", git("rev-parse", updated+":src/second.go"), nil, []string{"x.test/lib/sub"}},
		{"tools/both.go", "", git("rev-parse", updated+":tools/both.go"), nil, []string{pkg + "/a", "x.test/lib"}},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("changes = %#v\nwant %#v", got, want)
	}
	invalid := rule
	invalid.ExcludeDirs = []string{"tools/generated", "../outside"}
	if _, err := Compare(t.Context(), dir, old, updated, invalid); err == nil {
		t.Error("accepted an invalid second exclusion")
	}
	bad := commit()
	if _, err := Compare(t.Context(), dir, updated, bad, rule); err == nil {
		t.Error("malformed source reported as a clean scan")
	}
	write("tools/testdata/bad.go", "not Go: x.test/lib\n")
	if _, err := Compare(t.Context(), dir, bad, commit(), rule); err == nil {
		t.Error("malformed fixture mentioning another import root reported as a clean scan")
	}
}
