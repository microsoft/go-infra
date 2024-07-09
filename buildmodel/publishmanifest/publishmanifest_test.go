// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package publishmanifest

import (
	"bytes"
	"os"
	"testing"
)

// Test that reading a manifest with a BOM doesn't cause JSON parsing to fail.
func TestReadBOMManifest(t *testing.T) {
	data, err := os.ReadFile("testdata/msGo.output.manifest.json")
	if err != nil {
		t.Fatal(err)
	}
	_, err = Read(bytes.NewReader(data))
	if err != nil {
		t.Fatal(err)
	}
}
