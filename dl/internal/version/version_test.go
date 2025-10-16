// Copyright 2019 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package version

import (
	"bytes"
	"fmt"
	"strings"
	"testing"
)

func TestFormatted(t *testing.T) {
	var total int64 = 1
	var buff = new(bytes.Buffer)
	var units = []string{"B", "KB", "MB"}
	for i := 1; i < 4; i++ {
		pw := &progressWriter{w: nil, total: total, formatted: true, output: buff}
		pw.update()
		total *= 1024
		expected := fmt.Sprintf("%d %s", 1, units[i-1])
		if !strings.Contains(buff.String(), expected) {
			t.Errorf("expected: %s received: %s", expected, buff.String())
		}
	}
}

func TestUnFormatted(t *testing.T) {
	var total int64 = 1
	var buff = new(bytes.Buffer)
	for i := 1; i < 4; i++ {
		pw := &progressWriter{w: nil, total: total, formatted: false, output: buff}
		pw.update()
		expected := fmt.Sprintf("%d bytes", total)
		if !strings.Contains(buff.String(), expected) {
			t.Errorf("expected: %s received: %s", expected, buff.String())
		}
		total *= 1024
	}
}
