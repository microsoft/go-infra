// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package config

import (
	_ "embed"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestConfig(t *testing.T) {
	f, err := os.Open(filepath.FromSlash("../../config.json"))
	if os.IsNotExist(err) {
		t.Skip("config file not found")
	}
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
