// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

// The msgo1.26.0preview1 command runs the go command from Microsoft Go 1.26.0preview1.
//
// To install, run:
//
//	$ go install github.com/microsoft/go-lab/dl/msgo1.26.0preview1@latest
//	$ msgo1.26.0preview1 download
//
// And then use the msgo1.26.0preview1 command as if it were your normal go
// command.
package main

import "github.com/microsoft/go-infra/dl/internal/version"

func main() {
	version.RunCustom("msgo1.26.0preview1", "https://download.visualstudio.microsoft.com/download/pr/568421ae-4ba9-47c6-9230-73672b840ebf/45754d5866b65d6f67b0def57f04cc40/assets.json", "8882faa6841465cd92aa148e449d3dd54ec2d6f6eb066e8432d4a4825444c33b")
}
