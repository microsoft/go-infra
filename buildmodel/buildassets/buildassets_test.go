// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package buildassets

import (
	"io/ioutil"
	"os"
	"path"
	"testing"
)

func Test_getVersion(t *testing.T) {
	tests := []struct {
		name        string
		text        string
		wantVersion string
	}{
		{"Ordinary", "go1.17.3", "go1.17.3"},
		{"TrailingNewline", "go1.17.3\n", "go1.17.3"},
		{"MultilineFile", "go1.17.3\nMore information", "go1.17.3"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			filePath := path.Join(t.TempDir(), "VERSION")
			err := ioutil.WriteFile(filePath, []byte(tt.text), os.ModePerm)
			if err != nil {
				t.Error(err)
				return
			}

			if gotVersion := getVersion(filePath, "default"); gotVersion != tt.wantVersion {
				t.Errorf("getVersion() = %v, want %v", gotVersion, tt.wantVersion)
			}
		})
	}
}
