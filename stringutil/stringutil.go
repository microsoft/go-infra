// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package stringutil

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"golang.org/x/text/encoding/unicode"
	"golang.org/x/text/transform"
)

// CutPrefix behaves like strings.Cut, but only cuts a prefix, not anywhere in the string.
func CutPrefix(s, prefix string) (after string, found bool) {
	if strings.HasPrefix(s, prefix) {
		return s[len(prefix):], true
	}
	return s, false
}

// CutSuffix behaves like strings.Cut, but only cuts a suffix, not anywhere in the string.
func CutSuffix(s, suffix string) (before string, found bool) {
	if strings.HasSuffix(s, suffix) {
		return s[:len(s)-len(suffix)], true
	}
	return s, false
}

// CutTwice calls strings.Cut twice to split s into three strings. If either separator isn't found
// in s, returns s, "", "", false.
func CutTwice(s, sep1, sep2 string) (before, between, after string, found bool) {
	if before1, after1, found := strings.Cut(s, sep1); found {
		if between, after2, found := strings.Cut(after1, sep2); found {
			return before1, between, after2, true
		}
	}
	return s, "", "", false
}

// CutLast is [strings.Cut], but cutting at the last occurrence of sep rather than the first.
func CutLast(s, sep string) (before, after string, found bool) {
	if i := strings.LastIndex(s, sep); i != -1 {
		return s[:i], s[i+len(sep):], true
	}
	return "", s, false
}

// ReadJSONFile reads one JSON value from the specified file. Supports BOM.
func ReadJSONFile(path string, i any) (err error) {
	f, err := os.Open(path)
	if err != nil {
		return fmt.Errorf("unable to open JSON file %v for reading: %w", path, err)
	}
	defer func() {
		if closeErr := f.Close(); err == nil {
			err = closeErr
		}
	}()

	content := transform.NewReader(f, unicode.BOMOverride(transform.Nop))
	d := json.NewDecoder(content)
	if err := d.Decode(i); err != nil {
		return fmt.Errorf("unable to decode JSON file %v: %w", path, err)
	}
	return nil
}

// WriteJSONFile writes one specified value to a file as indented JSON with a trailing newline.
func WriteJSONFile(path string, i any) (err error) {
	f, err := os.Create(path)
	if err != nil {
		return fmt.Errorf("unable to open JSON file %v for writing: %w", path, err)
	}
	defer func() {
		if closeErr := f.Close(); err == nil {
			err = closeErr
		}
	}()

	d := json.NewEncoder(f)
	d.SetIndent("", "  ")
	if err := d.Encode(i); err != nil {
		return fmt.Errorf("unable to encode model into JSON file %v: %w", path, err)
	}
	return nil
}

// CRLFToLF is a [transform.Transformer] that converts all occurrences
// of "\r\n" to "\n", leaving lone '\r' (not followed by '\n') untouched.
type CRLFToLF struct{}

// Reset implements [transform.Transformer]. No state to clear.
func (CRLFToLF) Reset() {}

// Transform converts CRLF to LF.
// Implements [transform.Transformer].
// It's careful about chunk boundaries and dst capacity.
func (CRLFToLF) Transform(dst, src []byte, atEOF bool) (nDst, nSrc int, err error) {
	for nSrc < len(src) {
		// Need at least one byte of dst space.
		if nDst == len(dst) {
			return nDst, nSrc, transform.ErrShortDst
		}

		b := src[nSrc]

		if b == '\r' {
			// If '\r' is the last byte in src chunk and we are not at EOF,
			// request more source to decide if it's CRLF.
			if nSrc+1 == len(src) {
				if !atEOF {
					return nDst, nSrc, transform.ErrShortSrc
				}
				// We're already at EOF: leave lone '\r' alone.
				dst[nDst] = '\r'
				nDst++
				nSrc++
				continue
			}
			// We have at least one more byte, so check if we have a full \r\n.
			if src[nSrc+1] == '\n' {
				// Convert CRLF -> LF
				dst[nDst] = '\n'
				nDst++
				nSrc += 2
				continue
			}
			// Lone '\r' not followed by '\n': leave it alone.
			dst[nDst] = '\r'
			nDst++
			nSrc++
			continue
		}

		// Normal byte: copy.
		dst[nDst] = b
		nDst++
		nSrc++
	}

	return nDst, nSrc, nil
}
