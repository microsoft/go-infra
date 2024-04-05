// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package archive

import (
	"archive/zip"
	"io"
)

// UnzipOneFile extracts a single file from a zip archive.
// It returns the contents of the file, or nil if the file is not found.
func UnzipOneFile(name string, r io.ReaderAt, size int64) ([]byte, error) {
	z, err := zip.NewReader(r, size)
	if err != nil {
		return nil, err
	}
	for _, zf := range z.File {
		if zf.Name == name {
			rc, err := zf.Open()
			if err != nil {
				return nil, err
			}
			defer rc.Close()
			return io.ReadAll(rc)
		}
	}
	return nil, nil
}
