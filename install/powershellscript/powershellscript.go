// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package powershellscript

import _ "embed"

//go:embed microsoft-go-install.ps1
var Content string

const Name = "microsoft-go-install.ps1"
