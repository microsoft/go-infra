// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package main

import (
	"bytes"
	"crypto/sha256"
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

// checksums is copied from https://github.com/oras-project/oras/releases/download/v1.1.0/oras_1.1.0_checksums.txt.
var checksums = map[string]string{
	"oras_1.1.0_linux_s390x.tar.gz":  "067600d61d5d7c23f7bd184cff168ad558d48bed99f6735615bce0e1068b1d77",
	"oras_1.1.0_windows_amd64.zip":   "2ac83631181d888445e50784a5f760f7f9d97fba3c089e79b68580c496fe68cf",
	"oras_1.1.0_darwin_arm64.tar.gz": "d52d3140b0bb9f7d7e31dcbf2a513f971413769c11f7d7a5599e76cc98e45007",
	"oras_1.1.0_linux_armv7.tar.gz":  "def86e7f787f8deee50bb57d1c155201099f36aa0c6700d3b525e69ddf8ae49b",
	"oras_1.1.0_linux_amd64.tar.gz":  "e09e85323b24ccc8209a1506f142e3d481e6e809018537c6b3db979c891e6ad7",
	"oras_1.1.0_linux_arm64.tar.gz":  "e450b081f67f6fda2f16b7046075c67c9a53f3fda92fd20ecc59873b10477ab4",
	"oras_1.1.0_darwin_amd64.tar.gz": "f8ac5dea53dd9331cf080f1025f0612e7b07c5af864a4fd609f97d8946508e45",
}

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
	if err := downloadAndInstallOras(dir); err != nil {
		os.RemoveAll(dir) // Best effort to clean up the directory.
		return err
	}
	return nil
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
	if runtime.GOOS == "windows" {
		log.Printf("Add %#q to your PATH environment variable so that oras.exe can be found.\n", dir)
	}
	return nil
}

// download fetches the ORAS CLI from the GitHub release page.
func download(name string) ([]byte, error) {
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
	var dir string
	switch runtime.GOOS {
	case "windows":
		dir = filepath.Join(os.Getenv("USERPROFILE"), "bin")
	default:
		dir = "/usr/local/bin"
	}
	err := os.MkdirAll(dir, 0o755)
	return dir, err
}

func orasBinName() string {
	ext := ""
	if runtime.GOOS == "windows" {
		ext = ".exe"
	}
	return "oras" + ext
}
