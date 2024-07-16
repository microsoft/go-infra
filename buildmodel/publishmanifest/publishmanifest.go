// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package publishmanifest

import (
	"fmt"
)

// Manifest is a publish manifest file, written by DevDiv.MS.Go.Publishing.
type Manifest struct {
	Published []PublishedFile
}

// ByFilename returns a map of PublishedFile by filename for efficient lookup,
// or an error if there are duplicates.
func (m *Manifest) ByFilename() (map[string]PublishedFile, error) {
	result := make(map[string]PublishedFile, len(m.Published))
	for _, file := range m.Published {
		if _, ok := result[file.Filename]; ok {
			return nil, fmt.Errorf("duplicate filename: %s", file.Filename)
		}
		result[file.Filename] = file
	}
	return result, nil
}

// PublishedFile represents a file that has been published.
type PublishedFile struct {
	Filename string `json:"FileName"`
	SHA256   string `json:"Sha256"`
	URL      string `json:"Url"`
}
