// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package patch

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func TestMergeModuleInsertions(t *testing.T) {
	tests := []struct {
		name, kind, base, upstream, patched, want string
	}{
		{
			name: "require block", kind: "go.mod",
			base:     "require (\n\texample.com/z v1.0.0\n)\n",
			upstream: "require (\n\texample.com/z v1.1.0\n)\n",
			patched:  "require (\n\texample.com/a v1.0.0\n\texample.com/z v1.0.0\n)\n",
			want:     "require (\n\texample.com/a v1.0.0\n\texample.com/z v1.1.0\n)\n",
		},
		{
			name: "checksum", kind: "go.sum",
			base:     "example.com/z v1.0.0 h1:old\nexample.com/z v1.0.0/go.mod h1:oldmod\n",
			upstream: "example.com/z v1.1.0 h1:new\nexample.com/z v1.1.0/go.mod h1:newmod\n",
			patched:  "example.com/a v1.0.0 h1:added\nexample.com/z v1.0.0 h1:old\nexample.com/z v1.0.0/go.mod h1:oldmod\n",
			want:     "example.com/a v1.0.0 h1:added\nexample.com/z v1.1.0 h1:new\nexample.com/z v1.1.0/go.mod h1:newmod\n",
		},
		{
			name: "vendor manifest", kind: "modules.txt",
			base:     "# example.com/z v1.0.0\n## explicit; go 1.26\nexample.com/z\n",
			upstream: "# example.com/z v1.1.0\n## explicit; go 1.26\nexample.com/z\n",
			patched:  "# example.com/a v1.0.0\n## explicit; go 1.26\nexample.com/a\n# example.com/z v1.0.0\n## explicit; go 1.26\nexample.com/z\n",
			want:     "# example.com/a v1.0.0\n## explicit; go 1.26\nexample.com/a\n# example.com/z v1.1.0\n## explicit; go 1.26\nexample.com/z\n",
		},
		{
			name: "same dependency edited", kind: "go.mod",
			base:     "require example.com/z v1.0.0\n",
			upstream: "require example.com/z v1.1.0\n",
			patched:  "require example.com/z v1.2.0\nrequire example.com/a v1.0.0\n",
		},
		{
			name: "upstream changes dependency path", kind: "go.mod",
			base:     "require example.com/z v1.0.0\n",
			upstream: "require example.com/b v1.1.0\n",
			patched:  "require example.com/a v1.0.0\nrequire example.com/z v1.0.0\n",
		},
		{
			name: "upstream adds lines", kind: "go.mod",
			base:     "require example.com/z v1.0.0\n",
			upstream: "require example.com/b v1.0.0\nrequire example.com/z v1.1.0\n",
			patched:  "require example.com/a v1.0.0\nrequire example.com/z v1.0.0\n",
		},
		{
			name: "ambiguous insertion", kind: "go.mod",
			base:     "\nrequire example.com/z v1.0.0\n",
			upstream: "\nrequire example.com/z v1.1.0\n",
			patched:  "\nrequire example.com/a v1.0.0\n\nrequire example.com/z v1.0.0\n",
		},
		{
			name: "checksum changed without version", kind: "go.sum",
			base:     "example.com/z v1.0.0 h1:old\n",
			upstream: "example.com/z v1.0.0 h1:new\n",
			patched:  "example.com/a v1.0.0 h1:added\nexample.com/z v1.0.0 h1:old\n",
		},
		{
			name: "checksum kind changed", kind: "go.sum",
			base:     "example.com/z v1.0.0 h1:old\n",
			upstream: "example.com/z v1.1.0/go.mod h1:new\n",
			patched:  "example.com/a v1.0.0 h1:added\nexample.com/z v1.0.0 h1:old\n",
		},
		{
			name: "vendor metadata changed", kind: "modules.txt",
			base:     "# example.com/z v1.0.0\n## explicit; go 1.26\n",
			upstream: "# example.com/z v1.1.0\n## explicit; go 1.27\n",
			patched:  "# example.com/a v1.0.0\n# example.com/z v1.0.0\n## explicit; go 1.26\n",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := mergeModuleInsertions(tt.kind, tt.base, tt.upstream, tt.patched)
			if ok != (tt.want != "") || got != tt.want {
				t.Fatalf("mergeModuleInsertions = %q, %v; want %q, %v", got, ok, tt.want, tt.want != "")
			}
		})
	}
}

func TestModuleVersionUpdate(t *testing.T) {
	tests := []struct {
		kind, before, after string
		want                bool
	}{
		{"go.mod", "require example.com/z v1.0.0", "require example.com/z v1.1.0", true},
		{"go.mod", "\texample.com/z v1.0.0 // indirect", "\texample.com/z v1.1.0 // indirect", true},
		{"go.mod", "\texample.com/z v1.0.0 // indirect", "\texample.com/z v1.1.0 // changed", false},
		{"go.mod", "go 1.26.0", "go 1.27.0", false},
		{"go.mod", "example.com/z invalid", "example.com/z v1.1.0", false},
		{"go.sum", "example.com/z v1.0.0 h1:old", "example.com/z v1.1.0 invalid", false},
		{"modules.txt", "# example.com/z => ./local", "# example.com/z => ./other", false},
		{"file.txt", "example.com/z v1.0.0", "example.com/z v1.1.0", false},
	}
	for _, tt := range tests {
		t.Run(fmt.Sprintf("%s/%s/%s", tt.kind, tt.before, tt.after), func(t *testing.T) {
			if got := moduleVersionUpdate(tt.kind, tt.before, tt.after); got != tt.want {
				t.Fatalf("moduleVersionUpdate = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestTryThreeWayRebaseModuleUpdates(t *testing.T) {
	setThreeWayTestGitEnv(t)
	for _, preserveIndex := range []bool{false, true} {
		for _, conflict := range []string{"", "same dependency", "other file", "deleted file", "later patch"} {
			t.Run(fmt.Sprintf("index=%v/conflict=%s", preserveIndex, conflict), func(t *testing.T) {
				dir := t.TempDir()
				runThreeWayTestGit(t, dir, "init")
				originals := map[string]string{
					"src/cmd/go.mod":             "module example.com/cmd\n\ngo 1.26\n\nrequire (\n\texample.com/a v1.0.0\n\texample.com/z v1.0.0\n)\n",
					"src/cmd/go.sum":             "example.com/a v1.0.0 h1:a\nexample.com/z v1.0.0 h1:old\nexample.com/z v1.0.0/go.mod h1:oldmod\n",
					"src/cmd/vendor/modules.txt": "# example.com/a v1.0.0\n## explicit; go 1.26\nexample.com/a\n# example.com/z v1.0.0\n## explicit; go 1.26\nexample.com/z\n",
					"file.txt":                   "old\n",
				}
				write := func(name, content string) {
					t.Helper()
					full := filepath.Join(dir, filepath.FromSlash(name))
					if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
						t.Fatal(err)
					}
					writeThreeWayTestFile(t, full, content)
				}
				for name, content := range originals {
					write(name, content)
				}
				runThreeWayTestGit(t, dir, "add", ".")
				runThreeWayTestGit(t, dir, "commit", "-m", "base")
				base := threeWayTestGitOutput(t, dir, "rev-parse", "HEAD")
				patched := map[string]string{
					"src/cmd/go.mod": strings.Replace(originals["src/cmd/go.mod"], "\texample.com/z", "\texample.com/m v1.0.0\n\texample.com/z", 1),
					"src/cmd/go.sum": strings.Replace(originals["src/cmd/go.sum"], "example.com/z", "example.com/m v1.0.0 h1:m\nexample.com/z", 1),
					"src/cmd/vendor/modules.txt": strings.Replace(originals["src/cmd/vendor/modules.txt"], "# example.com/z",
						"# example.com/m v1.0.0\n## explicit; go 1.26\nexample.com/m\n# example.com/z", 1),
				}
				if conflict == "same dependency" {
					patched["src/cmd/go.mod"] = strings.ReplaceAll(patched["src/cmd/go.mod"], "example.com/z v1.0.0", "example.com/z v1.2.0")
				}
				if conflict == "other file" {
					patched["file.txt"] = "patched\n"
				}
				for name, content := range patched {
					write(name, content)
				}
				runThreeWayTestGit(t, dir, "commit", "-am", "add dependencies")
				patchDir := filepath.Join(dir, "patches")
				runThreeWayTestGit(t, dir, "format-patch", "--full-index", "-1", "-o", patchDir)
				patchPath := filepath.Join(patchDir, "0001-add-dependencies.patch")
				runThreeWayTestGit(t, dir, "reset", "--hard", base)
				for name, content := range originals {
					if name == "src/cmd/go.sum" && conflict == "deleted file" {
						continue
					}
					content = strings.NewReplacer("example.com/z v1.0.0", "example.com/z v1.1.0", "h1:old", "h1:new").Replace(content)
					if name == "file.txt" && conflict != "" {
						content = "upstream\n"
					}
					write(name, content)
				}
				if conflict == "deleted file" {
					runThreeWayTestGit(t, dir, "rm", "src/cmd/go.sum")
				}
				runThreeWayTestGit(t, dir, "commit", "-am", "update upstream")
				newBase := threeWayTestGitOutput(t, dir, "rev-parse", "HEAD")
				runThreeWayTestGit(t, dir, "checkout", "-b", "sync-target")
				writeThreeWayTestFile(t, filepath.Join(dir, ConfigFileName), `{"SubmoduleDir":"go","PatchesDir":"patches"}`)
				// Reproduce the adjacent-edit failure without the recovery logic.
				cmd := exec.Command("git", "am", "--3way", patchPath)
				cmd.Dir = dir
				if out, err := cmd.CombinedOutput(); err == nil {
					t.Fatalf("expected ordinary three-way merge to conflict: %s", out)
				}
				if unmerged := threeWayTestGitOutput(t, dir, "ls-files", "--unmerged"); !strings.Contains(unmerged, "src/cmd/go.mod") {
					t.Fatalf("expected module conflict, got %q", unmerged)
				}
				runThreeWayTestGit(t, dir, "am", "--abort")

				tracked := []string{patchPath}
				if conflict == "later patch" {
					second, _ := addPatchAfterFirst(t, dir, newBase, patchPath, "file.txt", "second patch\n")
					tracked = append(tracked, second)
				}
				if preserveIndex {
					indexPath, _ := enableThreeWayTestIndexPreservation(t, dir, tracked...)
					tracked = append(tracked, indexPath)
				}
				before := make(map[string]string)
				for _, name := range tracked {
					content, err := os.ReadFile(name)
					if err != nil {
						t.Fatal(err)
					}
					before[name] = string(content)
				}

				updated, err := TryThreeWayRebase(dir, "go", newBase)
				if err != nil {
					t.Fatal(err)
				}
				if conflict != "" {
					if len(updated) != 0 {
						t.Fatalf("unresolved conflict changed files: %v", updated)
					}
					for name, content := range before {
						ensureThreeWayTestFileChanged(t, name, []byte(content), false)
					}
					return
				}
				wantUpdated := []string{filepath.Join("patches", filepath.Base(patchPath))}
				if preserveIndex {
					wantUpdated = append(wantUpdated, filepath.Join("patches", IndexFileName))
				}
				if !slices.Equal(updated, wantUpdated) {
					t.Fatalf("updated = %v, want %v", updated, wantUpdated)
				}
				runThreeWayTestGit(t, dir, "-c", "am.threeWay=false", "am", patchPath)
				for name, content := range patched {
					want := strings.NewReplacer("example.com/z v1.0.0", "example.com/z v1.1.0", "h1:old", "h1:new").Replace(content)
					got, err := os.ReadFile(filepath.Join(dir, filepath.FromSlash(name)))
					if err != nil {
						t.Fatal(err)
					}
					if string(got) != want {
						t.Errorf("%s = %q, want %q", name, got, want)
					}
				}
			})
		}
	}
}
