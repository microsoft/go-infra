// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

// Package importdiff compares Git revisions and reports changed Go files that
// directly import any of the selected packages or their subpackages.
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
	SourceRoots []string // Search all of these slash-separated Git directories.
	ImportRoots []string // Match any of these import paths or their subpackages.
	ExcludeDirs []string // Skip files under any of these slash-separated directories.
}

func (r Rule) Validate() error {
	if len(r.SourceRoots) == 0 || len(r.ImportRoots) == 0 {
		return errors.New("at least one source root and one import root are required")
	}
	for _, root := range r.SourceRoots {
		if !fs.ValidPath(root) {
			return fmt.Errorf("invalid source root %q", root)
		}
	}
	for _, root := range r.ImportRoots {
		if !fs.ValidPath(root) || root == "." {
			return fmt.Errorf("invalid import root %q", root)
		}
	}
	for _, dir := range r.ExcludeDirs {
		if !fs.ValidPath(dir) {
			return fmt.Errorf("invalid exclude directory %q", dir)
		}
	}
	return nil
}

func (r Rule) excludes(name string) bool {
	return slices.ContainsFunc(r.ExcludeDirs, func(dir string) bool {
		return dir == "." || strings.HasPrefix(name, dir+"/")
	})
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
	if err := rule.Validate(); err != nil {
		return nil, err
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
	args := []string{"diff-tree", "-r", "-z", "--raw", "--no-commit-id", "--no-abbrev", "--no-renames", oldID, newID, "--"}
	out, err := git(append(args, rule.SourceRoots...)...).Output()
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
		if path.Ext(name) != ".go" || fields[2] == fields[3] || rule.excludes(name) {
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
			c.OldImports, err = readImports(reader, c.Path, c.OldBlob, rule.ImportRoots)
			if err != nil {
				return err
			}
			c.NewImports, err = readImports(reader, c.Path, c.NewBlob, rule.ImportRoots)
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

func readImports(r *bufio.Reader, name, id string, roots []string) ([]string, error) {
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
		for _, root := range roots {
			if p == root || strings.HasPrefix(p, root+"/") {
				imports = append(imports, p)
				break
			}
		}
	}
	if parseErr != nil {
		// Upstream includes intentionally malformed compiler/go command fixtures.
		if strings.Contains("/"+name, "/testdata/") && len(imports) == 0 {
			for _, root := range roots {
				if bytes.Contains(src, []byte(root)) {
					return nil, parseErr
				}
			}
			return nil, nil
		}
		return nil, parseErr
	}
	slices.Sort(imports)
	return imports, nil
}
