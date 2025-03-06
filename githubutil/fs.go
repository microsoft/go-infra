// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package githubutil

import (
	"context"
	"fmt"
	"io/fs"
	"time"

	"github.com/google/go-github/v65/github"
)

func NewRefFS(ctx context.Context, client *github.Client, owner, repo, ref string) *FS {
	return &FS{
		ctx:    ctx,
		client: client,
		owner:  owner,
		repo:   repo,
		ref:    ref,
	}
}

// SimplifiedFS specifies the methods from fs.ReadDirFS and fs.ReadFileFS without the Open method
// from fs.FS. fs.FS is general purpose, and not needed for current use cases of NewRefFS.
//
// Note that os.DirFS does implement SimplifiedFS, so SimplifiedFS can be used as a common way to
// read the local FS and also GitHub.
type SimplifiedFS interface {
	// ReadDir reads the named directory
	// and returns a list of directory entries sorted by filename.
	ReadDir(name string) ([]fs.DirEntry, error)

	// ReadFile reads the named file and returns its contents.
	// A successful call returns a nil error, not io.EOF.
	// (Because ReadFile reads the whole file, the expected EOF
	// from the final Read is not treated as an error to be reported.)
	//
	// The caller is permitted to modify the returned byte slice.
	// This method should return a copy of the underlying data.
	ReadFile(name string) ([]byte, error)
}

type FS struct {
	ctx    context.Context
	client *github.Client
	owner  string
	repo   string
	ref    string
}

var _ SimplifiedFS = (*FS)(nil)

func (r *FS) ReadFile(name string) ([]byte, error) {
	fileContent, err := DownloadFile(r.ctx, r.client, r.owner, r.repo, r.ref, name)
	if err != nil {
		return nil, fmt.Errorf("failed to download file %q: %w", name, err)
	}
	return fileContent, nil
}

func (r *FS) ReadDir(name string) ([]fs.DirEntry, error) {
	contents, err := ListDirFiles(r.ctx, r.client, r.owner, r.repo, r.ref, name)
	if err != nil {
		return nil, fmt.Errorf("failed to list directory %q: %w", name, err)
	}

	entries := make([]fs.DirEntry, 0, len(contents))
	for _, content := range contents {
		entry := &FSDirEntry{
			name: *content.Name,
			size: int64(content.GetSize()),
		}
		if content.GetType() == "dir" {
			entry.mode = fs.ModeDir
		}
		entries = append(entries, entry)
	}
	return entries, nil
}

type FSDirEntry struct {
	name string
	mode fs.FileMode
	size int64
}

func (r *FSDirEntry) Name() string {
	return r.name
}

func (r *FSDirEntry) IsDir() bool {
	return r.mode&fs.ModeDir != 0
}

func (r *FSDirEntry) Type() fs.FileMode {
	return r.mode
}

func (r *FSDirEntry) Info() (fs.FileInfo, error) {
	return r, nil
}

func (r *FSDirEntry) Size() int64 {
	return r.size
}

func (r *FSDirEntry) Mode() fs.FileMode {
	return r.mode
}

func (r *FSDirEntry) ModTime() time.Time {
	return time.Time{}
}

func (r *FSDirEntry) Sys() any {
	return nil
}
