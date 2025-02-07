// File: cmd/json2junit/main_test.go
package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/microsoft/go-infra/goldentest"
)

func TestRun(t *testing.T) {
	dir := filepath.Join("testdata", "inputs")
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
			if err := run(in, tmpOut); err != nil {
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
