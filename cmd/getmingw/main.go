// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package main

import (
	"crypto/sha512"
	_ "embed"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"

	"github.com/microsoft/go-infra/executil"
	"github.com/microsoft/go-infra/subcmd"
)

const description = `
Gets specific builds of the MinGW toolchain.

Intended for dev scenarios and CI tests. Not a complete list of builds or versions.
`

//go:embed data.json
var data []byte

const (
	Nixman      string = "nixman"
	Winlibs     string = "winlibs"
	Sourceforge string = "sourceforge"
)

const niXmanPrefix = "https://github.com/niXman/mingw-builds-binaries/releases/tag/"
const winlibsPrefix = "https://github.com/brechtsanders/winlibs_mingw/releases/download/"

var subcommands []subcmd.Option

func main() {
	if err := subcmd.Run("getmingw", description, subcommands); err != nil {
		log.Fatal(err)
	}
}

// Flags responsible for filtering results in multiple commands.
var (
	sources    subcmd.MultiStringFlag
	versions   subcmd.MultiStringFlag
	arches     subcmd.MultiStringFlag
	threadings subcmd.MultiStringFlag
	exceptions subcmd.MultiStringFlag
	runtimes   subcmd.MultiStringFlag
	llvms      subcmd.MultiStringFlag
)

func initFilterFlags() {
	flag.Var(&sources, "source", "source: see 'getmingw list'")
	flag.Var(&versions, "version", "version: see 'getmingw list'")
	flag.Var(&arches, "arch", "architecture: x86_64, i686")
	flag.Var(&threadings, "threading", "threading: posix, win32, mcf")
	flag.Var(&exceptions, "exception", "exception: seh, dwarf, sjlj")
	flag.Var(&runtimes, "runtime", "runtime: ucrt, msvcrt")
	flag.Var(&llvms, "llvm", "llvm build present: llvm, no")
}

func unmarshal() (r map[string]build, err error) {
	err = json.Unmarshal(data, &r)
	if err != nil {
		err = fmt.Errorf("failed to read embedded JSON: %v", err)
	}
	return r, err
}

func marshal(data map[string]build) ([]byte, error) {
	return json.MarshalIndent(data, "", "  ")
}

func filter(builds map[string]build) []*build {
	var result []*build
	match := func(msf *subcmd.MultiStringFlag, v string) bool {
		if len(msf.Values) == 0 {
			return true
		}
		for _, s := range msf.Values {
			if s == v {
				return true
			}
		}
		return false
	}
	for _, b := range builds {
		b := b
		if match(&sources, b.Source) &&
			match(&versions, b.Version) &&
			match(&arches, b.Arch) &&
			match(&threadings, b.Threading) &&
			match(&exceptions, b.Exception) &&
			match(&runtimes, b.Runtime) &&
			match(&llvms, b.LLVM) {

			result = append(result, &b)
		}
	}
	sort.Slice(result, func(i, j int) bool {
		return result[i].URL < result[j].URL
	})
	return result
}

func cacheDir() (string, error) {
	userDir, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(userDir, ".msft-go-infra", "getmingw"), nil
}

var errBuildNotFound = errors.New("build not found")

type build struct {
	Source    string
	Version   string
	Arch      string
	Threading string
	Exception string
	Runtime   string

	// LLVM is "llvm" or "no" for WinLibs. "" for other sources.
	LLVM string `json:",omitempty"`

	URL    string
	SHA512 string
}

func (b *build) CreateFreshChecksum() error {
	// Download the URL and compute the SHA512:
	var client http.Client
	resp, err := client.Get(b.URL)
	if err != nil {
		return err
	}
	if resp.StatusCode == http.StatusNotFound {
		return errBuildNotFound
	}
	defer resp.Body.Close()
	hash := sha512.New()
	if _, err := io.Copy(hash, resp.Body); err != nil {
		return err
	}
	// Write the SHA512 to the struct.
	b.SHA512 = fmt.Sprintf("%x", hash.Sum(nil))
	return nil
}

func (b *build) GetOrCreateCacheBinDir() (string, error) {
	mingwCacheDir, err := cacheDir()
	if err != nil {
		return "", err
	}
	buildDir := filepath.Join(mingwCacheDir, b.CacheKey())
	if err := os.MkdirAll(buildDir, 0o777); err != nil {
		return "", err
	}
	downloadFile := filepath.Join(buildDir, "mingw.7z")
	downloadedIndicator := filepath.Join(buildDir, ".downloaded")
	extractDir := filepath.Join(buildDir, "ext")
	extractedIndicator := filepath.Join(buildDir, ".extracted")

	var extractedBinDir string
	switch b.Arch {
	case "i686":
		extractedBinDir = filepath.Join(extractDir, "mingw32", "bin")
	case "x86_64":
		extractedBinDir = filepath.Join(extractDir, "mingw64", "bin")
	default:
		return "", fmt.Errorf("unknown arch %#q", b.Arch)
	}

	if cachedHash, err := os.ReadFile(downloadedIndicator); err != nil {
		if !errors.Is(err, os.ErrNotExist) {
			return "", fmt.Errorf("unexpected error while reading %#q: %v", downloadedIndicator, err)
		}
		// Best effort to delete old downloaded file, in case it failed in a weird way.
		_ = os.Remove(downloadFile)

		log.Printf("Downloading %v...", b.URL)
		// Download the URL and compute the SHA512:
		var client http.Client
		resp, err := client.Get(b.URL)
		if err != nil {
			return "", err
		}
		if resp.StatusCode != http.StatusOK {
			return "", fmt.Errorf("unexpected status code %v", resp.StatusCode)
		}
		defer resp.Body.Close()
		// Open the file for writing:
		f, err := os.OpenFile(downloadFile, os.O_CREATE|os.O_WRONLY, 0o666)
		if err != nil {
			return "", err
		}
		hash := sha512.New()
		_, err = io.Copy(io.MultiWriter(f, hash), resp.Body)
		if errClose := f.Close(); err == nil {
			err = errClose
		}
		if err != nil {
			return "", err
		}
		// Verify the SHA512:
		if sum := fmt.Sprintf("%x", hash.Sum(nil)); sum != b.SHA512 {
			return "", fmt.Errorf("SHA512 mismatch.\n  Expected: %v\n  Downloaded: %v", b.SHA512, sum)
		}
		// Write the download complete indicator:
		if err := os.WriteFile(downloadedIndicator, []byte(b.SHA512), 0o666); err != nil {
			return "", err
		}
	} else {
		// Found an indicator. But was there a collision in the key?
		if string(cachedHash) != b.SHA512 {
			return "", fmt.Errorf("SHA512 mismatch.\n  Expected: %v\n  Cached: %v", b.SHA512, string(cachedHash))
		}
	}
	if cachedHash, err := os.ReadFile(extractedIndicator); err != nil {
		if !errors.Is(err, os.ErrNotExist) {
			return "", fmt.Errorf("unexpected error while reading %#q: %v", extractedIndicator, err)
		}
		// Best effort to delete the old extraction dir, in case it failed in a weird way.
		_ = os.RemoveAll(extractDir)

		log.Printf("Extracting %#q...", downloadFile)
		// Extract the 7z file:
		cmd := exec.Command("7z", "x", "-o"+extractDir, downloadFile)
		// If the user cancels, or one 7z processes of many fails, make sure
		// all others are canceled. Otherwise, they may keep running in the
		// background.
		if out, err := executil.CombinedOutput(cmd); err != nil {
			return "", fmt.Errorf("failed to extract: %v, output: %v", err, out)
		}
		// Write the extraction complete indicator:
		if err := os.WriteFile(extractedIndicator, []byte(b.SHA512), 0o666); err != nil {
			return "", err
		}
		log.Printf("Extracted to %v", extractDir)
	} else {
		// Found an indicator. But was there a collision in the key?
		if string(cachedHash) != b.SHA512 {
			return "", fmt.Errorf("SHA512 mismatch.\n  Expected: %v\n  Cached: %v", b.SHA512, string(cachedHash))
		}
	}
	return extractedBinDir, nil
}

func (b *build) CacheKey() string {
	url := b.URL
	const maxUrlChars = 30
	if len(url) > maxUrlChars {
		url = url[len(url)-maxUrlChars:]
	}
	var u strings.Builder
	for _, c := range url {
		if (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9') {
			u.WriteRune(c)
		} else {
			u.WriteRune('_')
		}
	}
	u.WriteString("-")
	u.WriteString(b.SHA512[:32])
	return u.String()
}
