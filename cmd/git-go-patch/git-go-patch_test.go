// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/microsoft/go-infra/patch"
)

func TestLoadConfigMinimumToolVersion(t *testing.T) {
	previousRoot := *repoRootFlag
	t.Cleanup(func() { *repoRootFlag = previousRoot })
	tests := []struct {
		name    string
		minimum string
		wantErr bool
	}{
		{name: "supported", minimum: "v1.0.2"},
		{name: "too new", minimum: "v2.0.0", wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			*repoRootFlag = t.TempDir()
			data := `{"PreserveIndex":true,"MinimumToolVersion":"` + tt.minimum + `"}`
			if err := os.WriteFile(filepath.Join(*repoRootFlag, patch.ConfigFileName), []byte(data), 0o644); err != nil {
				t.Fatal(err)
			}
			config, err := loadConfig()
			if tt.wantErr {
				if err == nil || !strings.Contains(err.Error(), "tool version is lower than config file minimum version") {
					t.Fatalf("loadConfig error = %v, want tool upgrade requirement", err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if !config.PreserveIndex {
				t.Error("loadConfig did not enable index preservation")
			}
		})
	}
}
