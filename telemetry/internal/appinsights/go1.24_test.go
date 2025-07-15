// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

//go:build goexperiment.synctest && !go1.25

package appinsights_test

import (
	"testing"
	"testing/synctest"
)

func syncRun(t *testing.T, f func(*testing.T)) {
	synctest.Run(func() {
		// We need that t.Cleanup is called after the test function
		// has finished, but before sync.Run finishes.
		// This is because sync.Run will wait for all goroutines to finish,
		// and we want to ensure that the cleanup is done before that.
		// TODO: remove this file once go1.24 is no longer supported.
		t.Run(t.Name(), func(t *testing.T) {
			f(t)
		})
	})
}
