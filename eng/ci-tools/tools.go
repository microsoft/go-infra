// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

// Reference gotestsum to prevent it from being removed from go.mod by "go mod tidy".
// See https://github.com/golang/go/wiki/Modules#how-can-i-track-tool-dependencies-for-a-module

//go:build tools
// +build tools

package ci_tools

import _ "gotest.tools/gotestsum/cmd"
