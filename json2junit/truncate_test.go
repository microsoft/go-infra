// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package json2junit

import (
	"strings"
	"testing"
)

func TestTruncateForAzDO_NoTruncation(t *testing.T) {
	input := []byte("short output\nline two\n")
	got := truncateForAzDO(input)
	if string(got) != string(input) {
		t.Errorf("expected no change, got %q", got)
	}
}

func TestTruncateForAzDO_Empty(t *testing.T) {
	got := truncateForAzDO(nil)
	if len(got) != 0 {
		t.Errorf("expected empty, got %q", got)
	}
	got = truncateForAzDO([]byte{})
	if len(got) != 0 {
		t.Errorf("expected empty, got %q", got)
	}
}

func TestTruncateForAzDO_ExactlyAtLimit(t *testing.T) {
	// Content exactly at 4000 chars should not be truncated.
	input := []byte(strings.Repeat("b\n", azdoMaxChars/2))[:azdoMaxChars]
	got := truncateForAzDO(input)
	if string(got) != string(input) {
		t.Errorf("expected no change for content at exact limit, got len %d", len(got))
	}
}

func TestTruncateForAzDO_OneBeyondLimit(t *testing.T) {
	// Content one byte over 4000 triggers truncation with notice at front.
	input := []byte(strings.Repeat("c\n", (azdoMaxChars+2)/2))
	if len(input) <= azdoMaxChars {
		t.Fatalf("test setup: expected >%d bytes, got %d", azdoMaxChars, len(input))
	}

	got := truncateForAzDO(input)
	maxExpected := azdoMaxChars + len(beyondLimitSentinel)
	if len(got) > maxExpected {
		t.Errorf("output %d chars exceeds limit+sentinel %d", len(got), maxExpected)
	}
	if !strings.HasPrefix(string(got), string(truncationNotice)) {
		t.Error("expected truncation notice at beginning")
	}
	if !strings.HasSuffix(string(got), string(beyondLimitSentinel)) {
		t.Error("expected beyond-limit sentinel at end")
	}
}

func TestTruncateForAzDO_LongLinesShortened(t *testing.T) {
	// Long lines that push total over the limit.
	var lines []string
	for i := 0; i < 80; i++ {
		lines = append(lines, strings.Repeat("x", 500))
	}
	input := []byte(strings.Join(lines, "\n"))

	got := truncateForAzDO(input)
	maxExpected := azdoMaxChars + len(beyondLimitSentinel)
	if len(got) > maxExpected {
		t.Errorf("output %d chars exceeds limit+sentinel %d", len(got), maxExpected)
	}
	if !strings.HasPrefix(string(got), string(truncationNotice)) {
		t.Error("expected truncation notice at beginning")
	}
	if !strings.Contains(string(got), "[...]") {
		t.Error("expected line truncation marker [...]")
	}
	// Each truncated line should have at most maxLineLen + len("[...]") chars.
	for _, line := range strings.Split(string(got), "\n") {
		if len(line) > maxLineLen+len("[...]")+len(truncationNotice) {
			t.Errorf("line too long (%d chars): %s", len(line), line[:80])
		}
	}
}

func TestTruncateForAzDO_ShortLinesOverLimit(t *testing.T) {
	// Many short lines that exceed the limit. Lines should NOT be truncated
	// individually, but total output should be cut down.
	var lines []string
	for i := 0; i < 800; i++ {
		lines = append(lines, strings.Repeat("a", 30))
	}
	input := []byte(strings.Join(lines, "\n"))
	if len(input) <= azdoMaxChars {
		t.Fatalf("test input too short: %d", len(input))
	}

	got := truncateForAzDO(input)
	maxExpected := azdoMaxChars + len(beyondLimitSentinel)
	if len(got) > maxExpected {
		t.Errorf("output %d chars exceeds limit+sentinel %d", len(got), maxExpected)
	}
	if !strings.HasPrefix(string(got), string(truncationNotice)) {
		t.Error("expected truncation notice at beginning")
	}
	// No "[...]" markers since all lines are short.
	afterNotice := string(got)[len(truncationNotice):]
	if strings.Contains(afterNotice, "[...]") {
		t.Error("did not expect line truncation markers for short lines")
	}
}

func TestTruncateForAzDO_ShortLineNotTruncated(t *testing.T) {
	// A line under the limit within a long output should not get "[...]".
	shortLine := strings.Repeat("s", maxLineLen)
	longLine := strings.Repeat("L", 500)
	input := []byte(strings.Join([]string{shortLine, longLine, strings.Repeat("pad\n", 6000)}, "\n"))

	got := truncateForAzDO(input)
	// The short line should appear intact (no "[...]").
	if !strings.Contains(string(got), shortLine) {
		t.Error("expected short line to be preserved intact")
	}
	// The long line should be truncated.
	if !strings.Contains(string(got), strings.Repeat("L", maxLineLen)+"[...]") {
		t.Error("expected long line to be truncated with [...]")
	}
}

func TestTruncateForAzDO_EnvLinesShortened(t *testing.T) {
	// When over the limit, env var lines should be shortened to NAME=".../last".
	pathLine := "PATH=/usr/bin:/usr/local/bin:/home/user/.local/bin"
	gopathLine := "GOPATH=/home/user/go"
	importantLine := "--- FAIL: TestImportant (0.00s)"
	// Pad to exceed the limit.
	padding := strings.Repeat("output line\n", 1500)
	input := []byte(strings.Join([]string{importantLine, pathLine, gopathLine, padding}, "\n"))
	if len(input) <= azdoMaxChars {
		t.Fatalf("test setup: expected >%d bytes, got %d", azdoMaxChars, len(input))
	}

	got := string(truncateForAzDO(input))

	// The important line should still be there.
	if !strings.Contains(got, "FAIL: TestImportant") {
		t.Error("expected important line to be preserved")
	}
	// Full PATH value should be gone.
	if strings.Contains(got, "/usr/bin:/usr/local") {
		t.Error("expected full PATH value to be shortened")
	}
	// Shortened versions should be present.
	if !strings.Contains(got, `PATH=".../bin"`) {
		t.Errorf("expected shortened PATH, got: %s", got)
	}
	if !strings.Contains(got, `GOPATH=".../go"`) {
		t.Errorf("expected shortened GOPATH, got: %s", got)
	}
}

func TestTruncateForAzDO_EnvLinesKeptUnderLimit(t *testing.T) {
	// When under the limit, env var lines should be left alone.
	input := []byte("PATH=/usr/bin\nGOPATH=/home/user/go\nimportant line\n")
	got := truncateForAzDO(input)
	if string(got) != string(input) {
		t.Error("expected no change when under limit")
	}
}

func TestTruncateForAzDO_EnvLineIndented(t *testing.T) {
	// Env var lines with leading whitespace should preserve the indent.
	indentedPath := "    PATH=/usr/bin:/opt/tools/bin"
	padding := strings.Repeat("x\n", 10000)
	input := []byte(indentedPath + "\n" + padding)
	if len(input) <= azdoMaxChars {
		t.Fatalf("test setup: expected >%d bytes", azdoMaxChars)
	}

	got := string(truncateForAzDO(input))
	if strings.Contains(got, "PATH=/usr/bin") {
		t.Error("expected full PATH to be shortened")
	}
	if !strings.Contains(got, `    PATH=".../bin"`) {
		t.Errorf("expected indented shortened PATH, got: %s", got)
	}
}

func TestShortenEnvLine(t *testing.T) {
	tests := []struct {
		name string
		line string
		want string
	}{
		{"colon_separated", "PATH=/a/b:/c/d/e", `PATH=".../e"`},
		{"semicolon_separated", `PATH=C:\Go\bin;C:\Windows`, `PATH=".../Windows"`},
		{"single_path", "GOROOT=/usr/local/go", `GOROOT=".../go"`},
		{"trailing_separator", "PATH=/a/b:", `PATH=".../..."`},
		{"just_value", "HOME=/home/user", `HOME=".../user"`},
		{"preserves_indent", "  TEMP=/tmp/build", `  TEMP=".../build"`},
		{"root_path", "HOME=/", `HOME=".../..."`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := string(shortenEnvLine([]byte(tt.line)))
			if got != tt.want {
				t.Errorf("shortenEnvLine(%q) = %q, want %q", tt.line, got, tt.want)
			}
		})
	}
}
