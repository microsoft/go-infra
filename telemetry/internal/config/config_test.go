// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package config

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestConfig(t *testing.T) {
	f, err := os.Open(filepath.FromSlash("../../config/config.json"))
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	var cfg UploadConfig
	d := json.NewDecoder(f)
	d.DisallowUnknownFields()
	if err := d.Decode(&cfg); err != nil {
		t.Fatal(err)
	}
}
