// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

//go:build go1.25

package appinsights_test

import (
	"testing"
	"testing/synctest"
)

func syncRun(t *testing.T, f func(*testing.T)) {
	synctest.Test(t, f)
}
