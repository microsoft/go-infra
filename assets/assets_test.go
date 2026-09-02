// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package assets

import (
	"encoding/json"
	"reflect"
	"testing"
)

func TestToolsetBuildJSON(t *testing.T) {
	value := &ToolsetBuild{
		Branch:  "release-branch.go1.24",
		BuildID: "12345",
		Version: "1.24.1-2",
		Arches: []*Arch{
			{
				Env:       &ArchEnv{GOARCH: "amd64", GOOS: "linux"},
				SHA256:    "binary-sha256",
				Supported: true,
				URL:       "https://example.com/go-linux-amd64.tar.gz",
			},
			{
				SHA256: "source-sha256",
				URL:    "https://example.com/go-src.tar.gz",
			},
		},
		GoSrcURL:    "https://example.com/go-src.tar.gz",
		GoSrcSHA256: "source-sha256",
	}
	want := `{"branch":"release-branch.go1.24","buildId":"12345","version":"1.24.1-2","arches":[{"env":{"GOARCH":"amd64","GOOS":"linux"},"sha256":"binary-sha256","supported":true,"url":"https://example.com/go-linux-amd64.tar.gz"},{"sha256":"source-sha256","url":"https://example.com/go-src.tar.gz"}],"goSrcURL":"https://example.com/go-src.tar.gz","goSrcSHA256":"source-sha256"}`

	testJSONRoundTrip(t, value, want, &ToolsetBuild{})
}

func TestBranchJSON(t *testing.T) {
	value := &Branch{
		Version:      "1.24",
		Stable:       true,
		LatestStable: true,
		Files: []*LatestLink{
			{
				Filename: "go1.24.1.src.tar.gz",
				Version:  "1.24.1-2",
				Kind:     Source,
				URL:      "https://aka.ms/golang/release/latest/go1.24.src.tar.gz",
			},
		},
	}
	want := `{"version":"1.24","stable":true,"latestStable":true,"files":[{"filename":"go1.24.1.src.tar.gz","version":"1.24.1-2","kind":"source","url":"https://aka.ms/golang/release/latest/go1.24.src.tar.gz"}]}`

	testJSONRoundTrip(t, value, want, &Branch{})
}

func testJSONRoundTrip(t *testing.T, value any, want string, decoded any) {
	t.Helper()

	got, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != want {
		t.Errorf("json.Marshal() = %s, want %s", got, want)
	}

	if err := json.Unmarshal([]byte(want), decoded); err != nil {
		t.Fatal(err)
	}
	if got := reflect.ValueOf(decoded).Elem().Interface(); !reflect.DeepEqual(got, reflect.ValueOf(value).Elem().Interface()) {
		t.Errorf("json.Unmarshal() = %#v, want %#v", got, value)
	}
}
