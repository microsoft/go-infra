// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package submodule

import (
	"reflect"
	"testing"
)

func TestResetUpdateCmdReference(t *testing.T) {
	tests := []struct {
		name      string
		reference string
		force     bool
		want      []string
	}{
		{
			name: "default",
			want: []string{"git", "submodule", "update", "--init"},
		},
		{
			name:      "reference",
			reference: "/reference/repo",
			want:      []string{"git", "submodule", "update", "--init", "--reference", "/reference/repo"},
		},
		{
			name:  "force",
			force: true,
			want:  []string{"git", "submodule", "update", "--init", "-f"},
		},
		{
			name:      "reference and force",
			reference: "/reference/repo",
			force:     true,
			want:      []string{"git", "submodule", "update", "--init", "--reference", "/reference/repo", "-f"},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			cmd := resetUpdateCmd("/repo", test.reference, test.force)
			if !reflect.DeepEqual(cmd.Args, test.want) {
				t.Errorf("resetUpdateCmd args: got %q, want %q", cmd.Args, test.want)
			}
		})
	}
}
