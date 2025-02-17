// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package json2junit

import (
	"io"
	"os"
	"path/filepath"
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
