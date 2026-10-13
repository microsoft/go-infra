// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package patch

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestPreserveIndexConfig(t *testing.T) {
	tests := []struct {
		name    string
		config  string
		enabled bool
		wantErr bool
	}{
		{name: "absent", config: `{}`},
		{name: "false", config: `{"PreserveIndex":false}`},
		{name: "enabled", config: `{"PreserveIndex":true,"MinimumToolVersion":"v1.0.2"}`, enabled: true},
		{name: "higher minimum", config: `{"PreserveIndex":true,"MinimumToolVersion":"v1.1.0"}`, enabled: true},
		{name: "missing minimum", config: `{"PreserveIndex":true}`, wantErr: true},
		{name: "older minimum", config: `{"PreserveIndex":true,"MinimumToolVersion":"v1.0.1"}`, wantErr: true},
		{name: "prerelease minimum", config: `{"PreserveIndex":true,"MinimumToolVersion":"v1.0.2-rc.1"}`, wantErr: true},
		{name: "invalid minimum", config: `{"PreserveIndex":true,"MinimumToolVersion":"invalid"}`, wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			root := t.TempDir()
			if err := os.WriteFile(filepath.Join(root, ConfigFileName), []byte(tt.config), 0o644); err != nil {
				t.Fatal(err)
			}
			config, err := FindAncestorConfig(root)
			if tt.wantErr {
				if err == nil || !strings.Contains(err.Error(), "PreserveIndex requires MinimumToolVersion >= v1.0.2") {
					t.Fatalf("FindAncestorConfig error = %v, want minimum version requirement", err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if config.PreserveIndex != tt.enabled {
				t.Errorf("PreserveIndex = %v, want %v", config.PreserveIndex, tt.enabled)
			}
		})
	}
	t.Run("conventional configuration", func(t *testing.T) {
		root := t.TempDir()
		for _, dir := range []string{"go", "patches"} {
			if err := os.Mkdir(filepath.Join(root, dir), 0o755); err != nil {
				t.Fatal(err)
			}
		}
		config, err := FindAncestorConfig(root)
		if err != nil {
			t.Fatal(err)
		}
		if config.PreserveIndex {
			t.Fatal("conventional configuration enabled index preservation")
		}
	})
}
