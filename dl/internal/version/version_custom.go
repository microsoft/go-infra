// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package version

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/microsoft/go-infra/buildmodel/buildassets"
)

// RunCustom is Run, but with a custom assetJSONURL.
func RunCustom(version, assetJSONURL, assetJSONSHA256Sum string) {
	log.SetFlags(0)

	root, err := goroot(version)
	if err != nil {
		log.Fatalf("%s: %v", version, err)
	}

	if len(os.Args) == 2 && os.Args[1] == "download" {
		if err := installCustom(root, version, assetJSONURL, assetJSONSHA256Sum); err != nil {
			log.Fatalf("%s: download failed: %v", version, err)
		}
		os.Exit(0)
	}

	if _, err := os.Stat(filepath.Join(root, unpackedOkay)); err != nil {
		log.Fatalf("%s: not downloaded. Run '%s download' to install to %v", version, version, root)
	}

	runGo(root)
}

// installCustom is install, but with a custom assetJSONURL.
func installCustom(targetDir, version, assetJSONURL, assetJSONSHA256Sum string) error {
	if _, err := os.Stat(filepath.Join(targetDir, unpackedOkay)); err == nil {
		log.Printf("%s: already downloaded in %v", version, targetDir)
		return nil
	}

	if err := os.MkdirAll(targetDir, 0755); err != nil {
		return err
	}

	assetURL := assetJSONURL
	assetJSON, err := slurpURLToString(assetURL)
	if err != nil {
		return fmt.Errorf("error downloading %v: %v", assetURL, err)
	}
	assetJSONSum := sha256.Sum256([]byte(assetJSON))
	assetJSONSumHex := fmt.Sprintf("%x", assetJSONSum[:])
	if assetJSONSHA256Sum != "" && assetJSONSumHex != assetJSONSHA256Sum {
		return fmt.Errorf("SHA256 mismatch for %v: got %v, want %v (%q)", version, assetJSONSumHex, assetJSONSHA256Sum, assetURL)
	}

	var assets buildassets.BuildAssets
	if err := json.Unmarshal([]byte(assetJSON), &assets); err != nil {
		return fmt.Errorf("error unmarshalling %v: %v", assetURL, err)
	}

	var goURL, wantSHA string

	for _, a := range assets.Arches {
		if a.Env == nil {
			continue
		}
		if a.Env.GOOS == getOS() && a.Env.GOARCH == runtime.GOARCH {
			goURL = a.URL
			wantSHA = a.SHA256
			break
		}
	}
	if goURL == "" {
		return fmt.Errorf("no binary release of %v for %v/%v", version, getOS(), runtime.GOARCH)
	}

	res, err := http.Head(goURL)
	if err != nil {
		return err
	}

	if res.StatusCode == http.StatusNotFound {
		return fmt.Errorf("no binary release of %v for %v/%v at %v", version, getOS(), runtime.GOARCH, goURL)
	}
	if res.StatusCode != http.StatusOK {
		return fmt.Errorf("server returned %v checking size of %v", http.StatusText(res.StatusCode), goURL)
	}
	base := path.Base(goURL)
	archiveFile := filepath.Join(targetDir, base)
	if fi, err := os.Stat(archiveFile); err != nil || fi.Size() != res.ContentLength {
		if err != nil && !os.IsNotExist(err) {
			// Something weird. Don't try to download.
			return err
		}
		if err := copyFromURL(archiveFile, goURL); err != nil {
			return fmt.Errorf("error downloading %v: %v", goURL, err)
		}
		fi, err = os.Stat(archiveFile)
		if err != nil {
			return err
		}
		if fi.Size() != res.ContentLength {
			return fmt.Errorf("downloaded file %s from URL %s has size %v, which doesn't match server size %v", archiveFile, goURL, fi.Size(), res.ContentLength)
		}
	}

	if err := verifySHA256(archiveFile, strings.TrimSpace(wantSHA)); err != nil {
		return fmt.Errorf("error verifying SHA256 of %v: %v", archiveFile, err)
	}
	log.Printf("Unpacking %v ...", archiveFile)
	if err := unpackArchive(targetDir, archiveFile); err != nil {
		return fmt.Errorf("extracting archive %v: %v", archiveFile, err)
	}
	if err := os.WriteFile(filepath.Join(targetDir, unpackedOkay), nil, 0644); err != nil {
		return err
	}
	log.Printf("Success. You may now run '%v'", version)
	return nil
}
