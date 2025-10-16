// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package mkdl

import (
	"crypto/sha256"
	_ "embed"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strings"
)

//go:embed template.nogo
var template string

func PackageDir(v string) string {
	return filepath.Join("dl", "msgo"+v)
}

func MainPath(v string) string {
	return filepath.Join(PackageDir(v), "main.go")
}

func Has(v string) (bool, error) {
	_, err := os.Stat(MainPath(v))
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return false, nil
		}
		return false, err
	}
	return true, nil
}

func Generate(v string) error {
	log.Println("Processing:", v)
	if !strings.HasPrefix(v, "1.") {
		return fmt.Errorf("version %q does not start with '1.'; pass versions as '1.23.4-5' for example", v)
	}

	url := assetJSONURL("go" + v)
	log.Println("Fetching URL:", url)
	data, err := downloadString(url)
	if err != nil {
		return err
	}
	sum := sha256.Sum256([]byte(data))

	content := template
	content = strings.ReplaceAll(content, "{{version}}", v)
	content = strings.ReplaceAll(content, "{{sum}}", fmt.Sprintf("%x", sum))

	dir := PackageDir(v)
	mainPath := MainPath(v)

	// Sanity check to check if we're in the right dir.
	dirAbs, err := filepath.Abs(dir)
	if err != nil {
		return err
	}
	if b2 := filepath.Base(filepath.Dir(dirAbs)); b2 != "dl" {
		return fmt.Errorf("expected directory to be in 'dl', got %q (base of %q)", b2, dirAbs)
	}
	// Part of the sanity check. Pick an arbitrary package that should
	// definitely already exist in the target dir.
	hasAnotherRelease, err := Has("1.25.3-1")
	if err != nil {
		return fmt.Errorf("checking for existing package: %v", err)
	}
	if !hasAnotherRelease {
		return fmt.Errorf("package not found for version 1.25.3-1. Wrong execution dir?")
	}

	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("failed to create directory: %v", err)
	}
	if err := os.WriteFile(mainPath, []byte(content), 0o644); err != nil {
		return fmt.Errorf("failed to write file: %v", err)
	}
	log.Println("File written successfully:", mainPath)
	return nil
}

func assetJSONURL(version string) string {
	return "https://aka.ms/golang/release/latest/" + strings.TrimPrefix(version, "ms") + ".assets.json"
}

func downloadString(url string) (string, error) {
	res, err := http.Get(url)
	if err != nil {
		return "", err
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return "", fmt.Errorf("%s: %v", url, res.Status)
	}
	s, err := io.ReadAll(res.Body)
	if err != nil {
		return "", fmt.Errorf("reading %s: %v", url, err)
	}
	return string(s), nil
}
