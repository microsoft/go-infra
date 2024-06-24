// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package main

import (
	"bytes"
	"testing"
)

func TestUpdateAzureLinux(t *testing.T) {

}

func TestUpdateSignaturesFileContent(t *testing.T) {
	tests := []struct {
		input        []byte
		msGoFilename string
		msGoRevision string
		version      string
		expected     []byte
	}{
		{
			input: []byte(`
%global ms_go_filename go1.22.4-20240604.2.src.tar.gz
%global ms_go_revision 1
Version: 1.22.3
`),
			msGoFilename: "go1.23.0-20240605.3.src.tar.gz",
			msGoRevision: "2",
			version:      "1.23.0",
			expected: []byte(`
%global ms_go_filename go1.23.0-20240605.3.src.tar.gz
%global ms_go_revision 2
Version: 1.23.0
`),
		},
		{
			input: []byte(`
%global ms_go_filename go1.21.3-20230501.1.src.tar.gz
%global ms_go_revision 3
Version: 1.21.3
`),
			msGoFilename: "go1.24.0-20240606.4.src.tar.gz",
			msGoRevision: "4",
			version:      "1.24.0",
			expected: []byte(`
%global ms_go_filename go1.24.0-20240606.4.src.tar.gz
%global ms_go_revision 4
Version: 1.24.0
`),
		},
	}

	for _, tt := range tests {
		result := updateSignaturesFile(tt.input, tt.msGoFilename, tt.msGoRevision, tt.version)
		if !bytes.Equal(result, tt.expected) {
			t.Errorf("replaceContent(%q, %q, %q, %q) = %q, expected %q",
				tt.input, tt.msGoFilename, tt.msGoRevision, tt.version, result, tt.expected)
		}
	}
}
