// Copyright 2023 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

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
