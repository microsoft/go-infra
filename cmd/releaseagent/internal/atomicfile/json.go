// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

// Package atomicfile provides strict, private local JSON persistence.
package atomicfile

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
)

// ReadJSON decodes exactly one JSON value and rejects unknown fields.
func ReadJSON(path string, maxSize int64, target any) error {
	file, err := os.Open(path)
	if err != nil {
		return err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return err
	}
	if info.Size() > maxSize {
		return fmt.Errorf("file is %d bytes, maximum is %d", info.Size(), maxSize)
	}
	decoder := json.NewDecoder(io.LimitReader(file, maxSize))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return errors.New("file must contain exactly one JSON value")
	}
	return nil
}

// WriteJSON atomically replaces path with one indented JSON value and private permissions.
func WriteJSON(path, temporaryPattern string, maxSize int64, value any) error {
	directory := filepath.Dir(path)
	if err := os.MkdirAll(directory, 0o700); err != nil {
		return err
	}
	temporary, err := os.CreateTemp(directory, temporaryPattern)
	if err != nil {
		return err
	}
	temporaryPath := temporary.Name()
	keepTemporary := false
	defer func() {
		if !keepTemporary {
			_ = os.Remove(temporaryPath)
		}
	}()
	if err := temporary.Chmod(0o600); err != nil {
		_ = temporary.Close()
		return err
	}
	encoder := json.NewEncoder(temporary)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(value); err != nil {
		_ = temporary.Close()
		return err
	}
	info, err := temporary.Stat()
	if err != nil {
		_ = temporary.Close()
		return err
	}
	if info.Size() > maxSize {
		_ = temporary.Close()
		return fmt.Errorf("encoded file is %d bytes, maximum is %d", info.Size(), maxSize)
	}
	if err := temporary.Sync(); err != nil {
		_ = temporary.Close()
		return err
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	if err := os.Rename(temporaryPath, path); err != nil {
		return err
	}
	keepTemporary = true
	if err := os.Chmod(path, 0o600); err != nil {
		return err
	}
	if runtime.GOOS == "windows" {
		return nil
	}
	dir, err := os.Open(directory)
	if err != nil {
		return err
	}
	defer dir.Close()
	return dir.Sync()
}
