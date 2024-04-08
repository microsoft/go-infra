// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package archive

import (
	"archive/tar"
	"compress/gzip"
	"io"
)

// UntarOneFile extracts a single file from a tar archive.
// It returns the contents of the file, or nil if the file is not found.
func UntarOneFile(name string, r io.Reader, isGzipped bool) ([]byte, error) {
	if isGzipped {
		var err error
		r, err = gzip.NewReader(r)
		if err != nil {
			return nil, err
		}
	}
	tr := tar.NewReader(r)
	for {
		header, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, err
		}
		if header.Name == name {
			return io.ReadAll(tr)
		}
	}
	return nil, nil
}
