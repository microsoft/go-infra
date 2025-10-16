// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

// The msgo1.25.3-1 command runs the go command from the Microsoft build of Go 1.25.3-1.
//
// To install, run:
//
//	$ go install github.com/microsoft/go-lab/dl/msgo1.25.3-1@latest
//	$ msgo1.25.3-1 download
//
// And then use the msgo1.25.3-1 command as if it were your normal go command.
//
// See the release notes at https://github.com/microsoft/go/releases/tag/v1.25.3-1.
//
// File bugs at https://github.com/microsoft/go/issues/new.
package main

import "github.com/microsoft/go-infra/dl/internal/version"

func main() {
	version.Run("msgo1.25.3-1", "ab757f506e73c3081844e309a4f6fc71447a5b64c858b771b2192658ffad25d9")
}
