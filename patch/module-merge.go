// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package patch

import (
	"fmt"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strings"

	"github.com/microsoft/go-infra/gitcmd"
	"golang.org/x/mod/semver"
)

// resolveModuleConflicts handles insertion-only patches adjacent to upstream dependency version
// updates. It uses index stages rather than conflict markers, which depend on Git configuration.
func resolveModuleConflicts(dir string) (bool, error) {
	unmerged, err := gitcmd.CombinedOutput(dir, "ls-files", "--unmerged", "-z")
	if err != nil {
		return false, err
	}
	files := make(map[string]map[string]string)
	for _, entry := range strings.Split(unmerged, "\x00") {
		if entry == "" {
			continue
		}
		info, name, ok := strings.Cut(entry, "\t")
		fields := strings.Fields(info)
		if !ok || len(fields) != 3 {
			return false, fmt.Errorf("unexpected unmerged index entry %q", entry)
		}
		if fields[0] != "100644" || !filepath.IsLocal(name) {
			return false, nil
		}
		if files[name] == nil {
			files[name] = make(map[string]string)
		}
		files[name][fields[2]] = fields[1]
	}
	if len(files) == 0 {
		return false, nil
	}

	resolved := make(map[string]string)
	for name, stages := range files {
		kind := path.Base(name)
		if kind != "go.mod" && kind != "go.sum" && !(kind == "modules.txt" && path.Base(path.Dir(name)) == "vendor") {
			return false, nil
		}
		var contents [3]string
		for i, stage := range []string{"1", "2", "3"} {
			hash, ok := stages[stage]
			if !ok {
				return false, nil // Creation/deletion conflicts need manual resolution.
			}
			contents[i], err = gitcmd.Show(dir, hash)
			if err != nil {
				return false, fmt.Errorf("failed to read merge stage %s for %q: %w", stage, name, err)
			}
		}
		merged, ok := mergeModuleInsertions(kind, contents[0], contents[1], contents[2])
		if !ok {
			return false, nil
		}
		resolved[name] = merged
	}

	// Do not stage a partial resolution if any file requires manual intervention.
	for name, content := range resolved {
		if err := os.WriteFile(filepath.Join(dir, filepath.FromSlash(name)), []byte(content), 0o644); err != nil {
			return false, fmt.Errorf("failed to write resolved module file %q: %w", name, err)
		}
		if err := gitcmd.Run(dir, "add", "--", name); err != nil {
			return false, err
		}
	}
	return true, nil
}

func mergeModuleInsertions(kind, base, upstream, patched string) (string, bool) {
	baseLines := strings.SplitAfter(base, "\n")
	upstreamLines := strings.SplitAfter(upstream, "\n")
	patchLines := strings.SplitAfter(patched, "\n")
	if len(baseLines) != len(upstreamLines) || len(patchLines) <= len(baseLines) {
		return "", false
	}
	for i, line := range baseLines {
		if line != upstreamLines[i] && !moduleVersionUpdate(kind, line, upstreamLines[i]) {
			return "", false
		}
	}

	// Locate every base line in the insertion-only patch. Forward and backward matching must
	// agree so repeated lines cannot ambiguously position an insertion.
	positions := make([]int, len(baseLines))
	next := 0
	for i, line := range baseLines {
		offset := slices.Index(patchLines[next:], line)
		if offset < 0 {
			return "", false
		}
		positions[i] = next + offset
		next = positions[i] + 1
	}
	next = len(patchLines)
	for i := len(baseLines) - 1; i >= 0; i-- {
		for next--; next >= 0 && patchLines[next] != baseLines[i]; next-- {
		}
		if next != positions[i] {
			return "", false
		}
	}
	for i, position := range positions {
		patchLines[position] = upstreamLines[i]
	}
	return strings.Join(patchLines, ""), true
}

func moduleVersionUpdate(kind, base, upstream string) bool {
	before, after := strings.Fields(base), strings.Fields(upstream)
	if len(before) != len(after) {
		return false
	}
	version := 1
	switch kind {
	case "go.mod":
		if len(before) > 0 && before[0] == "require" {
			version = 2
		}
		if len(before) != version+1 && !(len(before) == version+3 && before[version+1] == "//" && before[version+2] == "indirect") {
			return false
		}
	case "go.sum":
		if len(before) != 3 {
			return false
		}
	case "modules.txt":
		if len(before) != 3 || before[0] != "#" {
			return false
		}
		version = 2
	default:
		return false
	}
	oldVersion, newVersion := before[version], after[version]
	if kind == "go.sum" {
		if strings.HasSuffix(oldVersion, "/go.mod") != strings.HasSuffix(newVersion, "/go.mod") ||
			!strings.HasPrefix(before[2], "h1:") || !strings.HasPrefix(after[2], "h1:") {
			return false
		}
		oldVersion = strings.TrimSuffix(oldVersion, "/go.mod")
		newVersion = strings.TrimSuffix(newVersion, "/go.mod")
		before[2] = after[2]
	}
	if oldVersion == newVersion || !semver.IsValid(oldVersion) || !semver.IsValid(newVersion) {
		return false
	}
	before[version] = after[version]
	return slices.Equal(before, after)
}
