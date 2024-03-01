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
