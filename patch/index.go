// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package patch

import (
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// IndexFileName is the generated file containing current blob hashes for patch files.
const IndexFileName = "index.json"

// Index preserves current index lines even when extract reuses an older, equivalent patch.
// Entries are keyed by patch filename and only used when the saved patch's checksum matches.
type Index map[string]indexEntry

type indexEntry struct {
	PatchSHA256 string            `json:"patchSHA256"`
	IndexLines  map[string]string `json:"indexLines"`
}

var indexLinePattern = regexp.MustCompile(`^index ([0-9a-f]{40}|[0-9a-f]{64})\.\.([0-9a-f]{40}|[0-9a-f]{64})( [0-7]{6})?$`)

func patchChecksum(content []byte) string {
	// Git may check out patch files with CRLF line endings on Windows.
	return fmt.Sprintf("%x", sha256.Sum256([]byte(strings.ReplaceAll(string(content), "\r\n", "\n"))))
}

// Record associates the saved patch with the index lines from the newly formatted patch.
// It returns false if the saved patch's diff headers cannot be matched to current's index lines.
func (index Index) Record(name string, saved []byte, current *Patch) bool {
	lines := make(map[string]string)
	rewriteIndexLines(current.Content, func(diff, line string) string {
		lines[diff] = line
		return line
	})
	matched := true
	rewriteIndexLines(string(saved), func(diff, line string) string {
		if _, ok := lines[diff]; !ok {
			matched = false
		}
		return line
	})
	if !matched {
		return false
	}
	index[name] = indexEntry{
		PatchSHA256: patchChecksum(saved),
		IndexLines:  lines,
	}
	return true
}

// WriteFile writes the index to dir in a deterministic, readable format.
func (index Index) WriteFile(dir string) error {
	data, err := json.MarshalIndent(index, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(dir, IndexFileName), append(data, '\n'), 0o644)
}

// ReadIndex reads the optional generated index from dir. Older patch sets need no index.
func ReadIndex(dir string) (Index, error) {
	path := filepath.Join(dir, IndexFileName)
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var index Index
	if err := json.Unmarshal(data, &index); err != nil {
		return nil, fmt.Errorf("failed to read patch index %q: %w", path, err)
	}
	return index, nil
}

// Restore updates only index lines in a matching patch. Missing or stale entries leave it unchanged.
func (index Index) Restore(name string, content []byte) ([]byte, error) {
	entry, ok := index[name]
	if !ok || entry.PatchSHA256 != patchChecksum(content) {
		return content, nil
	}
	for _, line := range entry.IndexLines {
		if !indexLinePattern.MatchString(line) {
			return nil, fmt.Errorf("invalid index line for patch %q: %q", name, line)
		}
	}
	return []byte(rewriteIndexLines(string(content), func(diff, line string) string {
		if replacement, ok := entry.IndexLines[diff]; ok {
			return replacement
		}
		return line
	})), nil
}

func rewriteIndexLines(content string, replace func(diff, line string) string) string {
	lines := strings.SplitAfter(content, "\n")
	var diff string
	inContent := false
	for i, text := range lines {
		line := strings.TrimSuffix(strings.TrimSuffix(text, "\n"), "\r")
		if line == "---" {
			inContent = true
		} else if inContent && strings.HasPrefix(line, "diff --git ") {
			diff = line
		} else if diff != "" && strings.HasPrefix(line, "index ") {
			lines[i] = replace(diff, line) + strings.TrimPrefix(text, line)
		}
	}
	return strings.Join(lines, "")
}
