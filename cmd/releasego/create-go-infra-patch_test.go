// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package main

import "testing"

func Test_nextModuleVersion(t *testing.T) {
	for _, tc := range []struct {
		name    string
		latest  string
		minor   bool
		want    string
		wantErr bool
	}{
		{
			name:   "advance minor resets patch",
			latest: "v1.3.7",
			minor:  true,
			want:   "v1.4.0",
		},
		{
			name:   "advance multi digit minor",
			latest: "v0.19.2",
			minor:  true,
			want:   "v0.20.0",
		},
		{
			name:   "first patch",
			latest: "v0.0.0",
			want:   "v0.0.1",
		},
		{
			name:   "multi digit patch",
			latest: "v0.0.12",
			want:   "v0.0.13",
		},
		{
			name:   "v1",
			latest: "v1.3.0",
			want:   "v1.3.1",
		},
		{
			name:   "minor",
			latest: "v0.1.0",
			want:   "v0.1.1",
		},
		{
			name:    "prerelease blocked",
			latest:  "v1.3.0-rc.1",
			wantErr: true,
		},
		{
			name:    "leading zero blocked",
			latest:  "v1.3.01",
			wantErr: true,
		},
		{
			name:    "patch overflow",
			latest:  "v1.3.9223372036854775807",
			wantErr: true,
		},
		{
			name:    "minor overflow",
			latest:  "v1.9223372036854775807.0",
			minor:   true,
			wantErr: true,
		},
		{
			name:    "invalid patch",
			latest:  "v0.0.x",
			wantErr: true,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := nextModuleVersion(tc.latest, tc.minor)
			if tc.wantErr {
				if err == nil {
					t.Fatal("expected error")
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got != tc.want {
				t.Fatalf("got %q, want %q", got, tc.want)
			}
		})
	}
}
