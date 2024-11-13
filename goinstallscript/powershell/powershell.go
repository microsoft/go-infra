// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

// Package powershell embeds the PowerShell script for installing Go.
package powershell

import (
	_ "embed"
)

//go:embed go-install.ps1
var Content string

const Name = "go-install.ps1"
