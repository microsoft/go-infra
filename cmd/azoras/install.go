// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package main

import (
	"bytes"
	"crypto/sha256"
	_ "embed"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"

	"github.com/microsoft/go-infra/internal/archive"
	"github.com/microsoft/go-infra/subcmd"
)

const (
	orasVersion = "1.1.0"
)

// checksums1_1_0 is copied from https://github.com/oras-project/oras/releases/download/v1.1.0/oras_1.1.0_checksums.txt.
//
//go:embed checksums/oras_1.1.0_checksums.txt
var checksums1_1_0 []byte

func init() {
	subcommands = append(subcommands, subcmd.Option{
		Name:        "install",
		Summary:     "Install the ORAS CLI.",
		Description: "",
		Handle:      handleInit,
	})
}

func handleInit(p subcmd.ParseFunc) error {
	if err := p(); err != nil {
		return err
	}
	if path, err := exec.LookPath("oras"); err == nil {
		log.Printf("ORAS CLI is already installed at %s\n", path)
		return nil
	}
	dir, err := installDir()
	if err != nil {
		return err
	}
	return downloadAndInstallOras(dir)
}

// downloadOras downloads the ORAS CLI and installs it in the recommended location.
// See https://oras.land/docs/installation/ for more information.
func downloadAndInstallOras(dir string) error {
	targetFile := orasGitHubFileName()
	content, err := download(targetFile)
	if err != nil {
		return err
	}
	return install(dir, content, filepath.Ext(targetFile))
}

// install installs the ORAS CLI in the given directory.
func install(dir string, content []byte, format string) error {
	log.Println("Extacting ORAS CLI...")
	name := orasBinName()
	content, err := extract(format, name, content)
	if err != nil {
		return err
	}
	log.Println("Extracted ORAS CLI")

	bin := filepath.Join(dir, name)
	log.Printf("Installing ORAS CLI to %#q...\n", bin)
	if err := os.WriteFile(bin, content, 0o755); err != nil {
		return err
	}
	log.Println("Installed ORAS CLI")
	log.Printf("Add %#q to your PATH environment variable so that oras can be found.\n", dir)
	return nil
}

// download fetches the ORAS CLI from the GitHub release page.
func download(name string) ([]byte, error) {
	checksums := parseChecksums(checksums1_1_0)
	expectedChecksum, ok := checksums[name]
	if !ok {
		return nil, fmt.Errorf("unable to find checksum for %s", name)
	}
	target := fmt.Sprintf("https://github.com/oras-project/oras/releases/download/v%s/%s", orasVersion, name)
	log.Printf("Downloading ORAS CLI from %s\n", target)
	resp, err := http.Get(target)
	if err != nil {
		return nil, fmt.Errorf("unable to download ORAS CLI: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("unable to download ORAS CLI: %s", resp.Status)
	}
	content, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("unable to read response body: %v", err)
	}

	// Validate the checksum
	sum := fmt.Sprintf("%x", sha256.Sum256(content))
	if sum != expectedChecksum {
		return nil, fmt.Errorf("SHA256 mismatch.\n  Expected: %v\n  Downloaded: %v", expectedChecksum, sum)
	}
	log.Println("Downloaded ORAS CLI")
	return content, nil
}

// extract extracts the ORAS CLI from the archive.
func extract(format string, name string, data []byte) (content []byte, err error) {
	switch format {
	case ".zip":
		content, err = archive.UnzipOneFile(name, bytes.NewReader(data), int64(len(data)))
	case ".tar.gz":
		content, err = archive.UntarOneFile(name, bytes.NewReader(data), true)
	default:
		return nil, fmt.Errorf("unsupported archive format: %s", format)
	}
	if err != nil {
		return nil, fmt.Errorf("unable to extract ORAS CLI: %v", err)
	}
	if content == nil {
		return nil, fmt.Errorf("unable to find %s in the archive", name)
	}
	return content, nil
}

func orasGitHubFileName() string {
	arch := runtime.GOARCH
	if arch == "arm" {
		// ORAS uses "armv7" instead of "arm" in the URL.
		arch = "armv7"
	}
	ext := "tar.gz"
	if runtime.GOOS == "windows" {
		ext = "zip"
	}
	return fmt.Sprintf("oras_%s_%s_%s.%s", orasVersion, runtime.GOOS, arch, ext)
}

func installDir() (string, error) {
	userDir, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	dir := filepath.Join(userDir, ".msft-go-infra", "oras")
	err = os.MkdirAll(dir, 0o755)
	if err != nil {
		return "", err
	}
	return dir, err
}

func orasBinName() string {
	ext := ""
	if runtime.GOOS == "windows" {
		ext = ".exe"
	}
	return "oras" + ext
}

func parseChecksums(data []byte) map[string]string {
	checksums := make(map[string]string)
	data = bytes.ReplaceAll(data, []byte("\r\n"), []byte("\n"))
	lines := bytes.Split(data, []byte("\n"))
	for _, line := range lines {
		parts := bytes.Fields(line)
		if len(parts) != 2 {
			continue
		}
		checksums[string(parts[1])] = string(parts[0])
	}
	return checksums
}
