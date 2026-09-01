// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package patch

import "github.com/microsoft/go-infra/executil"

// FormatPatch runs a deterministic "git format-patch" command with args appended and returns its
// stdout. If the command fails, stderr is included in the error.
func FormatPatch(dir string, args ...string) (string, error) {
	args = append([]string{
		"format-patch",
		// Set the minimum abbreviation level to a certain value to avoid user-specific defaults,
		// which may change due to Git version or user configuration.
		"--abbrev=14",
		// Remove default signature, which includes the Git version.
		"--signature=",
		// Use "From 0000000" instead of "From abc123f" in the patch file. A new commit hash is
		// generated each time the patches are applied, and including it in the patch text would
		// make the process less repeatable.
		"--zero-commit",
		// Remove "[PATCH 1/3]" from the patch file content. Avoid the reference to the total
		// number of patch files so earlier patch files don't change when a new one is appended.
		"--no-numbered",
	}, args...)
	return executil.Output(executil.Dir(dir, "git", args...))
}
