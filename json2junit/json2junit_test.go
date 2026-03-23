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

func TestConverterAttempt(t *testing.T) {
	in := filepath.Join("testdata", "inputs", "good", "pass.jsonl")

	tests := []struct {
		name          string
		opts          Options
		wantSuiteName string
	}{
		{"with_attempt", Options{JobAttempt: "3"}, "cmd/go [attempt 3]"},
		{"no_attempt", Options{}, "cmd/go"},
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
