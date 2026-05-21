// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package main

import "testing"

func Test_nextGoInfraPatchTag(t *testing.T) {
	for _, tc := range []struct {
		name    string
		latest  string
		want    string
		wantErr bool
	}{
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
			name:    "v1 blocked",
			latest:  "v1.0.0",
			wantErr: true,
		},
		{
			name:    "minor blocked",
			latest:  "v0.1.0",
			wantErr: true,
		},
		{
			name:    "invalid patch",
			latest:  "v0.0.x",
			wantErr: true,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := nextGoInfraPatchTag(tc.latest)
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
