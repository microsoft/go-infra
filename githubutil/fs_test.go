// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package githubutil

import (
	"os"
	"testing"
)

func TestSimplifiedFSImplementedByOSDirFS(t *testing.T) {
	_ = os.DirFS(".").(SimplifiedFS)
}
