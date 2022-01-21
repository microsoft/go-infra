// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package main

import (
	"strings"
	"testing"
)

func Test_createCommitMessageSnippet(t *testing.T) {
	maxUpstreamCommitMessageInSnippet = 20
	snippetCutoffIndicator = "[...]"

	tests := []struct {
		name    string
		message string
		want    string
	}{
		// Test snippet truncation.
		{"short", "Test message", "Test message"},
		{
			"near cutoff",
			"12345678901234567890",
			"12345678901234567890",
		},
		{
			"one past cutoff",
			"12345678901234567890-",
			"1234567890123456[...]",
		},
		{
			"three past cutoff",
			"12345678901234567890---",
			"1234567890123456[...]",
		},
		{"long", strings.Repeat("words ", 80), "words words word[...]"},

		// Test that snippet creation only takes the first line.
		{"newline", "PR Title\nContent", "PR Title"},
		{"newline Windows", "PR Title\r\nContent", "PR Title"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := createCommitMessageSnippet(tt.message); got != tt.want {
				t.Errorf("createCommitMessageSnippet() got = %v, want %v", got, tt.want)
			}
		})
	}
}
