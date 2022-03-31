// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package stringutil

import "strings"

// CutPrefix behaves like strings.Cut, but only cuts a prefix, not anywhere in the string.
func CutPrefix(s, prefix string) (after string, found bool) {
	if strings.HasPrefix(s, prefix) {
		return s[len(prefix):], true
	}
	return s, false
}
