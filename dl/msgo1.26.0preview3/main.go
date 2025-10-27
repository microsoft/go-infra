// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

// The msgo1.26.0preview3 command runs the go command from Microsoft Go 1.26.0preview3.
//
// To install, run:
//
//	$ go install github.com/microsoft/go-lab/dl/msgo1.26.0preview3@latest
//	$ msgo1.26.0preview3 download
//
// And then use the msgo1.26.0preview3 command as if it were your normal go
// command.
package main

import "github.com/microsoft/go-infra/dl/internal/version"

func main() {
	version.RunCustom("msgo1.26.0preview3", "https://download.visualstudio.microsoft.com/download/pr/459a1bc2-50ee-4bb9-9c84-d3ed8af0b2b5/50f992bfefbc7a8a87889dca6af851e9/assets.json", "98cf099cdad444a53d8e8b5440317d2fca5d39b251be18b7b9dc317c2b648140")
}
