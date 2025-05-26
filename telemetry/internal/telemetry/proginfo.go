// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package telemetry

import (
	"go/version"
	"os"
	"path/filepath"
	"runtime/debug"
	"strings"
)

// ProgramInfo extracts the go version and program package path to use for counter files.
//
// For programs in the Go toolchain, the program version will be the same as
// the Go version, and will typically be of the form "go1.2.3", not a semantic
// version of the form "v1.2.3". Go versions may also include spaces and
// special characters.
func ProgramInfo(info *debug.BuildInfo) (goVers, progPath string) {
	goVers = info.GoVersion
	if strings.Contains(goVers, "devel") || strings.Contains(goVers, "-") || !version.IsValid(goVers) {
		goVers = "devel"
	}

	progPath = info.Path
	if progPath == "" {
		progPath = strings.TrimSuffix(filepath.Base(os.Args[0]), ".exe")
	}

	return goVers, progPath
}
