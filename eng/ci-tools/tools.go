// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

// Reference gotestsum to prevent it from being removed from go.mod by "go mod tidy".
// See https://github.com/golang/go/wiki/Modules#how-can-i-track-tool-dependencies-for-a-module

// This file fails to build if 'tools' is set, because gotestsum is a program, not a library. It is
// necessary so "go install ..." works. There is no reason to build this file, anyway.

//go:build tools
// +build tools

package ci_tools

import _ "gotest.tools/gotestsum"
