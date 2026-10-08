// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package patch

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestIndexRestore(t *testing.T) {
	const saved = "Subject: change\n\n" +
		"diff --git a/file b/file\nindex abc..def 100644\n\n---\n" +
		"diff --git a/file b/file\nindex abc..def 100644\n" +
		"--- a/file\n+++ b/file\n@@ -1 +1 @@\n-index content\n+new content\n" +
		"diff --git \"a/other file\" \"b/other file\"\nindex 123..456\n" +
		"--- \"a/other file\"\n+++ \"b/other file\"\n@@ -1 +1 @@\n-old\n+new\n" +
		"diff --git a/mode b/mode\nold mode 100644\nnew mode 100755\n" +
		"diff --git a/binary b/binary\nindex 789..abc 100644\nGIT binary patch\nliteral 1\nA\n"
	first := "index " + strings.Repeat("a", 40) + ".." + strings.Repeat("b", 40) + " 100644"
	second := "index " + strings.Repeat("0", 40) + ".." + strings.Repeat("c", 40)
	binary := "index " + strings.Repeat("d", 64) + ".." + strings.Repeat("e", 64) + " 100644"
	_, body, _ := strings.Cut(saved, "\n---\n")
	current := strings.NewReplacer(
		"index abc..def 100644", first,
		"index 123..456", second,
		"index 789..abc 100644", binary,
	).Replace(body)
	index := make(Index)
	index.Record("change.patch", []byte(saved), &Patch{Content: "---\n" + current})
	want := strings.Split(saved, "\n---\n")[0] + "\n---\n" + current

	tests := []struct {
		name    string
		file    string
		content string
		want    string
	}{
		{"restores each file", "change.patch", saved, want},
		{"preserves checkout line endings", "change.patch", strings.ReplaceAll(saved, "\n", "\r\n"), strings.ReplaceAll(want, "\n", "\r\n")},
		{"missing entry", "other.patch", saved, saved},
		{"stale entry", "change.patch", saved + "+manual edit\n", saved + "+manual edit\n"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := index.Restore(tt.file, []byte(tt.content))
			if err != nil {
				t.Fatal(err)
			}
			if string(got) != tt.want {
				t.Errorf("restored patch:\n%s\nwant:\n%s", got, tt.want)
			}
		})
	}
	t.Run("unmatched diff header", func(t *testing.T) {
		index := make(Index)
		mismatched := strings.Replace(current, "diff --git a/file b/file", `diff --git "a/file" "b/file"`, 1)
		if index.Record("change.patch", []byte(saved), &Patch{Content: "---\n" + mismatched}) {
			t.Fatal("Record accepted unmatched diff headers")
		}
		if len(index) != 0 {
			t.Fatal("Record stored index lines for unmatched diff headers")
		}
	})
	t.Run("invalid index line", func(t *testing.T) {
		entry := index["change.patch"]
		entry.IndexLines["diff --git a/file b/file"] = first + "\n+injected"
		if _, err := index.Restore("change.patch", []byte(saved)); err == nil {
			t.Fatal("Restore accepted an injected patch line")
		}
	})
}

func TestIndexFile(t *testing.T) {
	dir := t.TempDir()
	index, err := ReadIndex(dir)
	if err != nil || index != nil {
		t.Fatalf("missing index = %v, %v; want nil, nil", index, err)
	}
	index = make(Index)
	index.Record("first.patch", []byte("first"), &Patch{})
	index.Record("second.patch", []byte("second"), &Patch{})
	if err := index.WriteFile(dir); err != nil {
		t.Fatal(err)
	}
	got, err := ReadIndex(dir)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, index) {
		t.Errorf("read index = %v, want %v", got, index)
	}
	path := filepath.Join(dir, IndexFileName)
	first, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := got.WriteFile(dir); err != nil {
		t.Fatal(err)
	}
	second, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(first) != string(second) {
		t.Error("rewriting the index changed its contents")
	}
	if err := os.WriteFile(path, []byte("{invalid"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadIndex(dir); err == nil {
		t.Fatal("ReadIndex accepted invalid JSON")
	}
}
