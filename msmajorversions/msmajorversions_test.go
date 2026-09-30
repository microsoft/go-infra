// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package msmajorversions

import (
	"encoding/json"
	"slices"
	"testing"

	"github.com/microsoft/go-infra/assets"
	"github.com/microsoft/go-infra/goldentest"
)

// The TestBranches golden data must change whenever major version advances. Provide a go:generate
// directive as a helper for devs and automation.
//go:generate go test . -run ^TestBranches$ -update

func TestBranches(t *testing.T) {
	got, err := json.MarshalIndent(struct {
		Previous assets.Branch `json:"previous"`
		Current  assets.Branch `json:"current"`
		Next     assets.Branch `json:"next"`
	}{
		Previous: Previous,
		Current:  Current,
		Next:     Next,
	}, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	goldentest.Check(t, "branches.golden.json", string(got)+"\n")
}

// The Windows arm64 cutoff is useful to preserve to demonstrate how platform support conditionals
// ought to be written. This test can be removed if it becomes cumbersome to maintain the condition.
func TestWindowsArm64Cutoff(t *testing.T) {
	tests := []struct {
		version          string
		wantWindowsArm64 bool
	}{
		{version: "1.25", wantWindowsArm64: false},
		{version: "1.26", wantWindowsArm64: true},
		{version: "1.27", wantWindowsArm64: true},
	}

	for _, test := range tests {
		t.Run(test.version, func(t *testing.T) {
			got := slices.Contains(platforms(test.version), "windows-arm64")
			if got != test.wantWindowsArm64 {
				t.Errorf("platforms(%q) contains windows-arm64 = %v, want %v", test.version, got, test.wantWindowsArm64)
			}
		})
	}
}
