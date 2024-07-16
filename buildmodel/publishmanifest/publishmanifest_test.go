// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package publishmanifest

import (
	"testing"

	"github.com/microsoft/go-infra/stringutil"
)

// Test that reading a manifest with a BOM doesn't cause JSON parsing to fail.
func TestReadBOMManifest(t *testing.T) {
	var m Manifest
	if err := stringutil.ReadJSONFile("testdata/msGo.output.manifest.json", &m); err != nil {
		t.Fatal(err)
	}
}
