// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package main

import (
	"bytes"
	"testing"
	"text/template"

	"github.com/microsoft/go-infra/goldentest"
)

func Test_dlTemplate(t *testing.T) {
	tmpl, err := template.New("dl").Parse(dlTemplate)
	if err != nil {
		t.Fatalf("error parsing dl template: %v", err)
	}

	tests := []struct {
		name string
		data dlVersionData
	}{
		{
			"single-version",
			dlVersionData{
				Version: "1.25.8-1",
				SHA256:  "3ff0e9fa6b16675d373521d805ead46e3fa74a70e8aadeb97848d30d5e19e562",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var buf bytes.Buffer
			if err := tmpl.Execute(&buf, tt.data); err != nil {
				t.Fatalf("error executing dl template: %v", err)
			}
			goldentest.Check(t, tt.name+".golden.go", buf.String())
		})
	}
}

func TestGenerateDLPRTitle(t *testing.T) {
	tests := []struct {
		versions []string
		expected string
	}{
		{nil, ""},
		{[]string{"1.25.8-1"}, "Add dl package for Go 1.25.8-1"},
		{[]string{"1.25.8-1", "1.26.1-1"}, "Add dl packages for Go 1.25.8-1 and 1.26.1-1"},
		{[]string{"1.25.8-1", "1.26.1-1", "1.24.3-1"}, "Add dl packages for Go 1.25.8-1, 1.26.1-1, and 1.24.3-1"},
	}

	for _, tt := range tests {
		t.Run(tt.expected, func(t *testing.T) {
			got := generateDLPRTitle(tt.versions)
			if got != tt.expected {
				t.Errorf("generateDLPRTitle(%v) = %q; want %q", tt.versions, got, tt.expected)
			}
		})
	}
}

func TestDLFilePath(t *testing.T) {
	tests := []struct {
		version  string
		expected string
	}{
		{"1.25.8-1", "dl/msgo1.25.8-1/main.go"},
		{"1.26.1-1", "dl/msgo1.26.1-1/main.go"},
	}

	for _, tt := range tests {
		t.Run(tt.version, func(t *testing.T) {
			got := dlFilePath(tt.version)
			if got != tt.expected {
				t.Errorf("dlFilePath(%q) = %q; want %q", tt.version, got, tt.expected)
			}
		})
	}
}
