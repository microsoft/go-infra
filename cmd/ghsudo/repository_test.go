// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package main

import (
	"strings"
	"testing"
)

func TestParseRemoteRepository(t *testing.T) {
	tests := []struct {
		name  string
		value string
		want  repository
		ok    bool
	}{
		{"ssh", "git@github.com:microsoft/go-infra.git", repository{"microsoft", "go-infra"}, true},
		{"https", "https://github.com/microsoft/go-infra.git", repository{"microsoft", "go-infra"}, true},
		{"sshURL", "ssh://git@github.com/microsoft/go-infra.git", repository{"microsoft", "go-infra"}, true},
		{"sshPort443", "ssh://git@ssh.github.com:443/v3/microsoft/go-infra.git", repository{"microsoft", "go-infra"}, true},
		{"nonGitHub", "https://dev.azure.com/dnceng/internal/_git/microsoft-go-infra", repository{}, false},
		{"tooManySegments", "https://github.com/microsoft/go-infra/extra", repository{}, false},
		{"invalid", "not a remote", repository{}, false},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, ok := parseGitHubRemote(test.value)
			if ok != test.ok || got != test.want {
				t.Fatalf("parseGitHubRemote(%q) = (%v, %v), want (%v, %v)", test.value, got, ok, test.want, test.ok)
			}
		})
	}
}

func TestParseRepository(t *testing.T) {
	for _, value := range []string{"microsoft/go-infra", "Azure/azure-sdk-for-go", "owner/repo.name"} {
		if _, err := parseRepository(value); err != nil {
			t.Errorf("parseRepository(%q): %v", value, err)
		}
	}
	for _, value := range []string{"", "repo", "/repo", "owner/", "owner/repo/extra", "-owner/repo", "owner/.."} {
		if _, err := parseRepository(value); err == nil {
			t.Errorf("parseRepository(%q) unexpectedly succeeded", value)
		}
	}
}

func TestSelectRepository(t *testing.T) {
	tests := []struct {
		name       string
		candidates []remoteCandidate
		want       repository
		wantError  string
	}{
		{
			name: "upstreamPreferred",
			candidates: []remoteCandidate{
				{"origin", repository{"someone", "go-infra"}},
				{"upstream", repository{"microsoft", "go-infra"}},
			},
			want: repository{"microsoft", "go-infra"},
		},
		{
			name: "originPreferredToOther",
			candidates: []remoteCandidate{
				{"mirror", repository{"mirror", "go-infra"}},
				{"origin", repository{"microsoft", "go-infra"}},
			},
			want: repository{"microsoft", "go-infra"},
		},
		{
			name: "duplicateURLs",
			candidates: []remoteCandidate{
				{"origin", repository{"microsoft", "go-infra"}},
				{"origin", repository{"Microsoft", "GO-INFRA"}},
			},
			want: repository{"Microsoft", "GO-INFRA"},
		},
		{
			name:      "none",
			wantError: "no github.com remote",
		},
		{
			name: "ambiguous",
			candidates: []remoteCandidate{
				{"one", repository{"microsoft", "one"}},
				{"two", repository{"microsoft", "two"}},
			},
			wantError: "multiple GitHub remotes",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := selectRepository(test.candidates)
			if test.wantError != "" {
				if err == nil || !strings.Contains(err.Error(), test.wantError) {
					t.Fatalf("error = %v, want containing %q", err, test.wantError)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if got != test.want {
				t.Fatalf("repository = %v, want %v", got, test.want)
			}
		})
	}
}
