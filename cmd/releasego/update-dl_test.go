// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestWriteDLManifests(t *testing.T) {
	repoDir := t.TempDir()
	assetsJSON := []byte("{\n  \"version\": \"1.25.8-1\"\n}\n")
	releases := []dlRelease{
		{Version: "1.25.8-1", AssetsJSON: assetsJSON},
	}

	if err := writeDLManifests(repoDir, releases); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(repoDir, dlManifestPath("1.25.8-1"))
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != string(assetsJSON) {
		t.Fatalf("manifest content changed:\ngot:  %q\nwant: %q", got, assetsJSON)
	}
	if err := writeDLManifests(repoDir, releases); err == nil {
		t.Fatal("writeDLManifests overwrote an existing manifest")
	}
}
