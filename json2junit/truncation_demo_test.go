// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package json2junit

import (
	"strings"
	"testing"
)

// TestTruncationDemo is a deliberately failing test that produces more than 4000
// characters of output. This exists to verify that the AzDO test viewer displays
// the truncation notice from truncateForAzDO. Delete this test after verifying.
func TestTruncationDemo(t *testing.T) {
	t.Log("=== This test deliberately fails to demonstrate AzDO truncation ===")
	t.Log("")
	for i := 0; i < 100; i++ {
		t.Logf("output line %03d: %s", i, strings.Repeat("abcdefgh", 5))
	}
	t.Log("")
	t.Logf("PATH=%s", strings.Repeat("/usr/local/very/long/path:", 20))
	t.Logf("GOPATH=%s", strings.Repeat("/home/user/go/path/segment:", 10))
	t.Log("")
	for i := 100; i < 200; i++ {
		t.Logf("output line %03d: %s", i, strings.Repeat("abcdefgh", 5))
	}
	t.Fatal("Deliberate failure to demonstrate truncation in AzDO test viewer. Delete json2junit/truncation_demo_test.go after verifying.")
}
