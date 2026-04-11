// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package json2junit

import (
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/microsoft/go-infra/goldentest"
)

func TestConverter(t *testing.T) {
	dir := filepath.Join("testdata", "inputs", "good")
	files, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, file := range files {
		fileName := file.Name()
		fileNameNoExt := fileName[:len(fileName)-len(filepath.Ext(fileName))]
		t.Run(fileNameNoExt, func(t *testing.T) {
			in := filepath.Join(dir, fileName)
			tmpOut := filepath.Join(t.TempDir(), "output.xml")
			if err := ConvertFile(tmpOut, in); err != nil {
				t.Fatal(err)
			}
			data, err := os.ReadFile(tmpOut)
			if err != nil {
				t.Fatal(err)
			}
			goldentest.Check(t, fileNameNoExt+".xml", string(data))
		})
	}
}

func TestConverterIncludePackage(t *testing.T) {
	in := filepath.Join("testdata", "inputs", "good", "pass.jsonl")
	tmpOut := filepath.Join(t.TempDir(), "output.xml")
	if err := ConvertFileWithOptions(tmpOut, in, &Options{
		IncludePackageInTestName: true,
	}); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(tmpOut)
	if err != nil {
		t.Fatal(err)
	}
	goldentest.Check(t, "pass.xml", string(data))
}

func TestConverterJobAttempt(t *testing.T) {
	in := filepath.Join("testdata", "inputs", "good", "pass.jsonl")

	tests := []struct {
		name          string
		opts          Options
		wantSuiteName string
	}{
		{"with_job_attempt", Options{JobAttempt: "3"}, "cmd/go [attempt 3]"},
		{"no_job_attempt", Options{}, "cmd/go"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tmpOut := filepath.Join(t.TempDir(), "output.xml")
			if err := ConvertFileWithOptions(tmpOut, in, &tt.opts); err != nil {
				t.Fatal(err)
			}
			data, err := os.ReadFile(tmpOut)
			if err != nil {
				t.Fatal(err)
			}
			// The suite name appears in the XML as: <testsuite name="...">
			want := `name="` + tt.wantSuiteName + `"`
			if !strings.Contains(string(data), want) {
				t.Errorf("expected suite name %q in output, got:\n%s", tt.wantSuiteName, data)
			}
		})
	}
}

func TestConverterErrors(t *testing.T) {
	dir := filepath.Join("testdata", "inputs", "bad")
	files, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, file := range files {
		fileName := file.Name()
		fileNameNoExt := fileName[:len(fileName)-len(filepath.Ext(fileName))]
		t.Run(fileNameNoExt, func(t *testing.T) {
			in, err := os.Open(filepath.Join(dir, fileName))
			if err != nil {
				t.Fatal(err)
			}
			err = Convert(io.Discard, in)
			if err == nil {
				t.Fatal("expected error")
			}
		})
	}
}

func TestWarnLongContent_NoWarning(t *testing.T) {
	input := []byte("short output\nline two\n")
	got := warnLongContent(input)
	if string(got) != string(input) {
		t.Errorf("expected no change, got %q", got)
	}
}

func TestWarnLongContent_Empty(t *testing.T) {
	got := warnLongContent(nil)
	if len(got) != 0 {
		t.Errorf("expected empty, got %q", got)
	}
	got = warnLongContent([]byte{})
	if len(got) != 0 {
		t.Errorf("expected empty, got %q", got)
	}
}

func TestWarnLongContent_ExactlyAtLimit(t *testing.T) {
	input := []byte(strings.Repeat("b\n", azdoMaxChars/2))[:azdoMaxChars]
	got := warnLongContent(input)
	if string(got) != string(input) {
		t.Errorf("expected no change for content at exact limit, got len %d", len(got))
	}
}

func TestWarnLongContent_OneBeyondLimit(t *testing.T) {
	input := []byte(strings.Repeat("c\n", (azdoMaxChars+2)/2))
	if len(input) <= azdoMaxChars {
		t.Fatalf("test setup: expected >%d bytes, got %d", azdoMaxChars, len(input))
	}

	got := warnLongContent(input)
	if !strings.HasPrefix(string(got), string(azdoWarning)) {
		t.Error("expected warning at beginning")
	}
	afterWarning := string(got)[len(azdoWarning):]
	if afterWarning != string(input) {
		t.Errorf("expected original content after warning, got len %d vs %d", len(afterWarning), len(input))
	}
}

func TestWarnLongContent_PreservesAllContent(t *testing.T) {
	var lines []string
	for i := 0; i < 1500; i++ {
		lines = append(lines, strings.Repeat("a", 30))
	}
	input := []byte(strings.Join(lines, "\n"))
	if len(input) <= azdoMaxChars {
		t.Fatalf("test input too short: %d", len(input))
	}

	got := warnLongContent(input)
	if !strings.HasPrefix(string(got), string(azdoWarning)) {
		t.Error("expected warning at beginning")
	}
	afterWarning := string(got)[len(azdoWarning):]
	if afterWarning != string(input) {
		t.Error("expected all content preserved without any truncation")
	}
}
