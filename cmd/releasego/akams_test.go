// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package main

import (
	"testing"

	"github.com/go-test/deep"
	"github.com/microsoft/go-infra/buildmodel/buildassets"
	"github.com/microsoft/go-infra/buildmodel/dockerversions"
)

func Test_createLinkPairs(t *testing.T) {
	latestShortLinkPrefix = "testing/"
	input := buildassets.BuildAssets{
		Branch:  "release-branch.go1.18",
		BuildID: "123456",
		Version: "1.17.7-1",
		Arches: []*dockerversions.Arch{
			{
				URL: "https://example.org/golang/build/1234.10/go.1234.10.linux-amd64.tar.gz",
			},
		},
		GoSrcURL: "https://example.org/golang/build/1234.10/go.1234.10.src.tar.gz",
	}
	want := []akaMSLinkPair{
		{Short: "testing/go1.17.linux-amd64.tar.gz", Target: "https://example.org/golang/build/1234.10/go.1234.10.linux-amd64.tar.gz"},
		{Short: "testing/go1.17.linux-amd64.tar.gz.sha256", Target: "https://example.org/golang/build/1234.10/go.1234.10.linux-amd64.tar.gz.sha256"},
		{Short: "testing/go1.17.linux-amd64.tar.gz.sig", Target: "https://example.org/golang/build/1234.10/go.1234.10.linux-amd64.tar.gz.sig"},
		{Short: "testing/go1.17.src.tar.gz", Target: "https://example.org/golang/build/1234.10/go.1234.10.src.tar.gz"},
		{Short: "testing/go1.17.src.tar.gz.sha256", Target: "https://example.org/golang/build/1234.10/go.1234.10.src.tar.gz.sha256"},
		{Short: "testing/go1.17.src.tar.gz.sig", Target: "https://example.org/golang/build/1234.10/go.1234.10.src.tar.gz.sig"},
		{Short: "testing/go1.17.assets.json", Target: "https://example.org/golang/build/1234.10/assets.json"},

		{Short: "testing/go1.17.7.linux-amd64.tar.gz", Target: "https://example.org/golang/build/1234.10/go.1234.10.linux-amd64.tar.gz"},
		{Short: "testing/go1.17.7.linux-amd64.tar.gz.sha256", Target: "https://example.org/golang/build/1234.10/go.1234.10.linux-amd64.tar.gz.sha256"},
		{Short: "testing/go1.17.7.linux-amd64.tar.gz.sig", Target: "https://example.org/golang/build/1234.10/go.1234.10.linux-amd64.tar.gz.sig"},
		{Short: "testing/go1.17.7.src.tar.gz", Target: "https://example.org/golang/build/1234.10/go.1234.10.src.tar.gz"},
		{Short: "testing/go1.17.7.src.tar.gz.sha256", Target: "https://example.org/golang/build/1234.10/go.1234.10.src.tar.gz.sha256"},
		{Short: "testing/go1.17.7.src.tar.gz.sig", Target: "https://example.org/golang/build/1234.10/go.1234.10.src.tar.gz.sig"},
		{Short: "testing/go1.17.7.assets.json", Target: "https://example.org/golang/build/1234.10/assets.json"},

		{Short: "testing/go1.17.7-1.linux-amd64.tar.gz", Target: "https://example.org/golang/build/1234.10/go.1234.10.linux-amd64.tar.gz"},
		{Short: "testing/go1.17.7-1.linux-amd64.tar.gz.sha256", Target: "https://example.org/golang/build/1234.10/go.1234.10.linux-amd64.tar.gz.sha256"},
		{Short: "testing/go1.17.7-1.linux-amd64.tar.gz.sig", Target: "https://example.org/golang/build/1234.10/go.1234.10.linux-amd64.tar.gz.sig"},
		{Short: "testing/go1.17.7-1.src.tar.gz", Target: "https://example.org/golang/build/1234.10/go.1234.10.src.tar.gz"},
		{Short: "testing/go1.17.7-1.src.tar.gz.sha256", Target: "https://example.org/golang/build/1234.10/go.1234.10.src.tar.gz.sha256"},
		{Short: "testing/go1.17.7-1.src.tar.gz.sig", Target: "https://example.org/golang/build/1234.10/go.1234.10.src.tar.gz.sig"},
		{Short: "testing/go1.17.7-1.assets.json", Target: "https://example.org/golang/build/1234.10/assets.json"},
	}
	got, err := createLinkPairs(input)
	if err != nil {
		t.Fatal(err)
	}
	if diff := deep.Equal(got, want); diff != nil {
		for _, d := range diff {
			t.Error(d)
		}
	}
}

func Test_makeFloatingFilename(t *testing.T) {
	type args struct {
		filename     string
		buildNumber  string
		floatVersion string
	}
	tests := []struct {
		name    string
		args    args
		want    string
		wantErr bool
	}{
		// Actual 1.21.0 names.
		{"1.21", args{"go.20230808.6.linux-amd64.tar.gz", "20230808.6", "1.21"}, "go1.21.linux-amd64.tar.gz", false},
		{"1.21 windows", args{"go.20230808.6.windows-amd64.zip", "20230808.6", "1.21"}, "go1.21.windows-amd64.zip", false},
		{"1.21 src", args{"go.20230808.6.src.tar.gz", "20230808.6", "1.21"}, "go1.21.src.tar.gz", false},
		{"1.21 assets", args{"assets.json", "20230808.6", "1.21"}, "go1.21.assets.json", false},

		// The naming change in 1.22 means we need to handle cases like these.
		{"1.22", args{"go1.22.0-1234.5.linux-amd64.tar.gz", "1234.5", "1.22"}, "go1.22.linux-amd64.tar.gz", false},
		{"1.22 windows", args{"go1.22.0-1234.5.windows-amd64.zip", "1234.5", "1.22"}, "go1.22.windows-amd64.zip", false},

		// Make sure names that don't fit the requirements are rejected.
		{"missing build number", args{"go1.22.0-.linux-amd64.tar.gz", "1234.5", "1.22.0"}, "", true},
		{"dev build", args{"go1.22-abc1234567-dev.linux-amd64.tar.gz", "1234.5", "1.22.0"}, "", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := makeFloatingFilename(tt.args.filename, tt.args.buildNumber, tt.args.floatVersion)
			if (err != nil) != tt.wantErr {
				t.Errorf("makeFloatingFilename() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if got != tt.want {
				t.Errorf("makeFloatingFilename() got = %v, want %v", got, tt.want)
			}
		})
	}
}

func Test_propsFileContent(t *testing.T) {
	pairs := []akaMSLinkPair{
		{
			Short: "from", Target: "to",
		},
		{
			Short:  "release/latest/go1.18.2-linux-amd64.tar.gz",
			Target: "https://example.org/go/go1.18.s-linux-amd64.tar.gz",
		},
	}
	want := `<Project>
  <ItemGroup>
    <AkaMSLink Include="from" TargetUrl="to"></AkaMSLink>
    <AkaMSLink Include="release/latest/go1.18.2-linux-amd64.tar.gz" TargetUrl="https://example.org/go/go1.18.s-linux-amd64.tar.gz"></AkaMSLink>
  </ItemGroup>
</Project>
`

	got, err := propsFileContent(pairs)
	if err != nil {
		t.Fatal(err)
	}
	if got != want {
		t.Errorf("propsFileContent() got %v, want %v", got, want)
	}
}
