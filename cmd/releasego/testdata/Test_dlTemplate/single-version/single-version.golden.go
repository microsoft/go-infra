// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

// The msgo1.25.8-1 command runs the go command from Microsoft Go 1.25.8-1.
//
// To install, run:
//
//	$ go install github.com/microsoft/go-lab/dl/msgo1.25.8-1@latest
//	$ msgo1.25.8-1 download
//
// And then use the go1.25.8-1 command as if it were your normal go
// command.
//
// See the release notes at https://github.com/microsoft/go/releases/tag/v1.25.8-1.
//
// File bugs at https://github.com/microsoft/go/issues/new.
package main

import "github.com/microsoft/go-lab/dl/internal/version"

func main() {
	version.Run("msgo1.25.8-1", "3ff0e9fa6b16675d373521d805ead46e3fa74a70e8aadeb97848d30d5e19e562")
}
