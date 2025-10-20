// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

// The msgo1.26.0preview2 command runs the go command from Microsoft Go 1.26.0preview2.
//
// To install, run:
//
//	$ go install github.com/microsoft/go-lab/dl/msgo1.26.0preview2@latest
//	$ msgo1.26.0preview2 download
//
// And then use the msgo1.26.0preview2 command as if it were your normal go
// command.
package main

import "github.com/microsoft/go-infra/dl/internal/version"

func main() {
	version.RunCustom("msgo1.26.0preview2", "https://download.visualstudio.microsoft.com/download/pr/3d0c0dd9-c0c4-47ab-a942-6504e1e801b2/22baabe36bb156930dd33ca02b5c0469/assets.json", "4366977816d4608e7c72d26421ae5c5e8182d644b05121aa316e8944093e944a")
}
