// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

// Package importdiff finds changed Go files which directly import a package
// or one of its subpackages, without checking out or building either revision.
package importdiff

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"go/parser"
	"go/token"
	"io"
	"io/fs"
	"os/exec"
	"path"
	"slices"
	"strconv"
	"strings"
)

type Rule struct {
	SourceRoot string // Slash-separated Git directory, e.g. "src".
	ImportRoot string // Match this import path and its subpackages.
	ExcludeDir string // Optional slash-separated source directory to skip.
}

type Change struct {
	Path string
	// A missing side has an empty blob ID. An existing side that doesn't
	// import a matching package has an empty import list.
	OldBlob, NewBlob       string
	OldImports, NewImports []string
}

// Compare reports net changes between two commits, ordered by upstream path.
// Renames are a deletion and an addition; both sides matter when an import is removed.
func Compare(ctx context.Context, repo, oldRev, newRev string, rule Rule) ([]Change, error) {
	if !fs.ValidPath(rule.SourceRoot) || !fs.ValidPath(rule.ImportRoot) || rule.ImportRoot == "." ||
		(rule.ExcludeDir != "" && !fs.ValidPath(rule.ExcludeDir)) {

		return nil, fmt.Errorf("invalid import diff rule: %+v", rule)
	}
	git := func(args ...string) *exec.Cmd {
		return exec.CommandContext(ctx, "git", append([]string{"-C", repo, "--literal-pathspecs"}, args...)...)
	}
	rev := func(ref string) (string, error) {
		out, err := git("rev-parse", "--verify", "--end-of-options", ref+"^{commit}").Output()
		if err != nil {
			return "", fmt.Errorf("resolve revision %q: %w", ref, err)
		}
		return strings.TrimSpace(string(out)), nil
	}
	oldID, err := rev(oldRev)
	if err != nil {
		return nil, err
	}
	newID, err := rev(newRev)
	if err != nil {
		return nil, err
	}
	out, err := git("diff-tree", "-r", "-z", "--raw", "--no-commit-id", "--no-abbrev", "--no-renames", oldID, newID, "--", rule.SourceRoot).Output()
	if err != nil {
		return nil, fmt.Errorf("diff upstream revisions: %w", err)
	}
	var changes []Change
	var blobs strings.Builder
	entry := string(out)
	for entry != "" {
		header, tail, ok := strings.Cut(entry, "\x00")
		name, rest, terminated := strings.Cut(tail, "\x00")
		fields := strings.Fields(header)
		if !ok || !terminated || len(fields) != 5 || !strings.HasPrefix(header, ":") {
			return nil, fmt.Errorf("unexpected git diff-tree record: %q", header)
		}
		entry = rest
		if path.Ext(name) != ".go" || strings.HasPrefix(name, rule.ExcludeDir+"/") || fields[2] == fields[3] {
			continue
		}
		blob := func(mode, id string) (string, error) {
			switch mode {
			case "000000":
				return "", nil
			case "100644", "100755":
				fmt.Fprintln(&blobs, id)
				return id, nil
			default:
				return "", fmt.Errorf("unexpected mode %s for Go source %s", mode, name)
			}
		}
		a, err := blob(strings.TrimPrefix(fields[0], ":"), fields[2])
		if err != nil {
			return nil, err
		}
		b, err := blob(fields[1], fields[3])
		if err != nil {
			return nil, err
		}
		changes = append(changes, Change{Path: name, OldBlob: a, NewBlob: b})
	}
	if len(changes) == 0 {
		return nil, nil
	}

	cat := git("cat-file", "--batch", "--buffer")
	cat.Stdin = strings.NewReader(blobs.String())
	r, err := cat.StdoutPipe()
	if err != nil {
		return nil, err
	}
	if err := cat.Start(); err != nil {
		return nil, err
	}
	reader := bufio.NewReader(r)
	read := func() error {
		for i := range changes {
			c := &changes[i]
			var err error
			c.OldImports, err = readImports(reader, c.Path, c.OldBlob, rule.ImportRoot)
			if err != nil {
				return err
			}
			c.NewImports, err = readImports(reader, c.Path, c.NewBlob, rule.ImportRoot)
			if err != nil {
				return err
			}
		}
		return nil
	}
	scanErr := read()
	_ = r.Close()
	if err := errors.Join(scanErr, cat.Wait()); err != nil {
		return nil, err
	}
	return slices.DeleteFunc(changes, func(c Change) bool {
		return len(c.OldImports)+len(c.NewImports) == 0
	}), nil
}

func readImports(r *bufio.Reader, name, id, root string) ([]string, error) {
	if id == "" {
		return nil, nil
	}
	var got string
	var size int
	if _, err := fmt.Fscanf(r, "%s blob %d\n", &got, &size); err != nil {
		return nil, fmt.Errorf("reading %s: %w", name, err)
	}
	if got != id || size < 0 || size > 64<<20 {
		return nil, fmt.Errorf("unexpected blob for %s: %s (%d bytes)", name, got, size)
	}
	src := make([]byte, size+1)
	if _, err := io.ReadFull(r, src); err != nil {
		return nil, fmt.Errorf("reading %s: %w", name, err)
	}
	if src[size] != '\n' {
		return nil, fmt.Errorf("missing blob terminator for %s", name)
	}
	file, parseErr := parser.ParseFile(token.NewFileSet(), name, src[:size], parser.ImportsOnly|parser.SkipObjectResolution)
	var imports []string
	for _, spec := range file.Imports {
		p, err := strconv.Unquote(spec.Path.Value)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", name, err)
		}
		if p == root || strings.HasPrefix(p, root+"/") {
			imports = append(imports, p)
		}
	}
	if parseErr != nil {
		// Upstream includes intentionally malformed compiler/go command fixtures.
		if strings.Contains("/"+name, "/testdata/") && len(imports) == 0 && !bytes.Contains(src, []byte(root)) {
			return nil, nil
		}
		return nil, parseErr
	}
	slices.Sort(imports)
	return imports, nil
}
