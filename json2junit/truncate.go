// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package json2junit

import "bytes"

const (
	// azdoMaxChars is the approximate maximum number of characters the AzDO
	// test viewer displays before silently truncating test failure content.
	azdoMaxChars = 16000
	// maxLineLen is the maximum number of characters kept per output line
	// when truncation is applied. Truncating long lines (e.g. PATH dumps)
	// increases the chance that meaningful test output fits within the AzDO
	// display limit.
	maxLineLen = 200
)

var truncationNotice = []byte("[json2junit: Output truncated at ~16000 characters. See raw test output for full content.]\n\n")

// envVarPrefixes lists environment variable prefixes whose values tend to be
// very long and not useful in a test failure context. When truncation is needed,
// these are shortened to just the variable name and the last path segment of the
// value, e.g. PATH=/a/b/c:/d/e/f becomes PATH=".../f".
var envVarPrefixes = [][]byte{
	[]byte("PATH="),
	[]byte("GOPATH="),
	[]byte("GOROOT="),
	[]byte("GOMODCACHE="),
	[]byte("GOCACHE="),
	[]byte("HOME="),
	[]byte("USERPROFILE="),
	[]byte("APPDATA="),
	[]byte("LOCALAPPDATA="),
	[]byte("PROGRAMFILES="),
	[]byte("TEMP="),
	[]byte("TMP="),
}

// shortenEnvLine takes a line like "  PATH=/a/b:/c/d" and returns something
// like "  PATH=\".../d\"". It preserves any leading whitespace from the
// original line.
func shortenEnvLine(line []byte) []byte {
	trimmed := bytes.TrimSpace(line)
	leading := line[:len(line)-len(trimmed)]

	eqIdx := bytes.IndexByte(trimmed, '=')
	if eqIdx < 0 {
		return line
	}
	name := trimmed[:eqIdx]
	value := trimmed[eqIdx+1:]

	// Find the last meaningful path segment. For PATH-like variables that use
	// : or ; as separators, take the last entry. Then take the last path
	// component of that entry.
	var last []byte
	if sepIdx := bytes.LastIndexAny(value, ":;"); sepIdx >= 0 {
		last = value[sepIdx+1:]
	} else {
		last = value
	}
	if slashIdx := bytes.LastIndexAny(last, "/\\"); slashIdx >= 0 {
		last = last[slashIdx+1:]
	}
	// If last is empty (e.g. trailing separator), just use "...".
	if len(last) == 0 {
		last = []byte("...")
	}

	// Build: <leading><NAME>=".../last"
	result := make([]byte, 0, len(leading)+len(name)+len(`=".../"`)+len(last))
	result = append(result, leading...)
	result = append(result, name...)
	result = append(result, []byte(`=".../`)...)
	result = append(result, last...)
	result = append(result, '"')
	return result
}

// truncateForAzDO truncates test output so it fits within AzDO's test viewer
// display limit. If the content is within the limit, it is returned as-is.
// Otherwise, it first shortens low-value env var lines (e.g. PATH=...),
// then truncates remaining long lines to maxLineLen chars, and finally trims
// the total output. A warning notice is prepended when any modification occurs.
func truncateForAzDO(content []byte) []byte {
	if len(content) <= azdoMaxChars {
		return content
	}

	lines := bytes.Split(content, []byte("\n"))

	// Pass 1: Shorten low-value env var lines.
	for i, line := range lines {
		trimmed := bytes.TrimSpace(line)
		for _, prefix := range envVarPrefixes {
			if bytes.HasPrefix(trimmed, prefix) {
				lines[i] = shortenEnvLine(line)
				break
			}
		}
	}

	// Pass 2: Truncate individual long lines.
	for i, line := range lines {
		if len(line) > maxLineLen {
			truncated := make([]byte, 0, maxLineLen+len("[...]"))
			truncated = append(truncated, line[:maxLineLen]...)
			truncated = append(truncated, "[...]"...)
			lines[i] = truncated
		}
	}
	content = bytes.Join(lines, []byte("\n"))

	// Pass 3: Hard-cap total length.
	contentLimit := azdoMaxChars - len(truncationNotice)
	if len(content) > contentLimit {
		content = content[:contentLimit]
	}

	result := make([]byte, 0, len(truncationNotice)+len(content))
	result = append(result, truncationNotice...)
	result = append(result, content...)
	return result
}
