// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package main

import (
	"cmp"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"maps"
	"net/http"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/microsoft/go-infra/buildmodel/buildassets"
	"github.com/microsoft/go-infra/cmd/mkdlpackage/internal/mkdl"
	"golang.org/x/sync/errgroup"
)

var (
	scrape   = flag.Bool("scrape", false, "Scrape Microsoft build of Go download links to fill in packages for missing releases.")
	versions = flag.String("versions", "", "A comma separated list of versions like '1.*' to generate based on the template. Skipped if '-scrape'.")
)

func main() {
	if err := run(); err != nil {
		log.Fatalln(err)
	}
}

type version struct {
	Major    int
	Minor    int
	Revision int
}

func (v version) String() string {
	return fmt.Sprintf("1.%d.%d-%d", v.Major, v.Minor, v.Revision)
}

var client = http.Client{
	Timeout: time.Minute,
}

func run() error {
	flag.Parse()

	if *scrape {
		return runScrape()
	}

	if *versions == "" {
		return errors.New("no versions specified")
	}
	for _, v := range strings.Split(*versions, ",") {
		if err := mkdl.Generate(v); err != nil {
			return err
		}
	}
	return nil
}

func runScrape() error {
	var checkEg errgroup.Group

	var checkedMu sync.Mutex
	checked := make(map[version]struct{})

	var versionsMu sync.Mutex
	versionsWithoutPackageAssets := make(map[version]*buildassets.BuildAssets)

	var goCheck func(v version)
	goCheck = func(v version) {
		checkEg.Go(func() error {
			checkedMu.Lock()
			_, alreadyChecked := checked[v]
			checked[v] = struct{}{}
			checkedMu.Unlock()
			if alreadyChecked {
				return nil
			}

			has, err := mkdl.Has(v.String())
			if err != nil {
				return fmt.Errorf("checking version %q: %w", v, err)
			}
			if !has {
				assets, err := downloadAssets(v)
				if err != nil {
					if errors.Is(err, ErrRedirNotExist) {
						log.Printf("Dead end: %q", v)
						return nil
					}
					return err
				}
				versionsMu.Lock()
				versionsWithoutPackageAssets[v] = assets
				versionsMu.Unlock()
			}
			goCheck(version{Major: v.Major, Minor: v.Minor, Revision: v.Revision + 1})
			goCheck(version{Major: v.Major, Minor: v.Minor + 1, Revision: 1})
			goCheck(version{Major: v.Major + 1, Minor: 0, Revision: 1})
			return nil
		})
	}
	goCheck(version{Major: 22, Minor: 0, Revision: 1})

	if err := checkEg.Wait(); err != nil {
		return fmt.Errorf("waiting for checks: %w", err)
	}

	versionsWithoutPackage := slices.SortedFunc(
		maps.Keys(versionsWithoutPackageAssets),
		func(a, b version) int {
			if a.Major != b.Major {
				return cmp.Compare(a.Major, b.Major)
			}
			if a.Minor != b.Minor {
				return cmp.Compare(a.Minor, b.Minor)
			}
			return cmp.Compare(a.Revision, b.Revision)
		},
	)

	var generateEg errgroup.Group
	for _, v := range versionsWithoutPackage {
		generateEg.Go(func() error {
			return mkdl.Generate(v.String())
		})
	}
	return generateEg.Wait()
}

var ErrRedirNotExist = errors.New("redirection doens't exist")

func downloadAssets(v version) (*buildassets.BuildAssets, error) {
	url := fmt.Sprintf(
		"https://aka.ms/golang/release/latest/go1.%d.%d-%d.assets.json",
		v.Major, v.Minor, v.Revision)

	log.Printf("Attempting to download assets file %q", url)

	resp, err := client.Get(url)
	if err != nil {
		return nil, fmt.Errorf("fetching assets for version %q: %w", v, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("unexpected status code %d for version %q", resp.StatusCode, v)
	}

	bodyData, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("reading assets for version %q: %w", v, err)
	}
	if bodyData[0] == '<' {
		// Unfortunately aka.ms gives us a OK bing.com page if we
		// attempt to fetch a version that doesn't exist.
		return nil, fmt.Errorf("version %q does not exist: %w", v, ErrRedirNotExist)
	}

	var assets buildassets.BuildAssets
	if err := json.Unmarshal(bodyData, &assets); err != nil {
		return nil, fmt.Errorf("decoding assets for version %q: %w", v, err)
	}

	return &assets, nil
}
