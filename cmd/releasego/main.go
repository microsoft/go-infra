// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package main

import (
	"flag"
	"log"
	"strings"

	"github.com/microsoft/go-infra/buildmodel/buildassets"
	"github.com/microsoft/go-infra/goversion"
	"github.com/microsoft/go-infra/subcmd"
)

const description = `
releasego runs various parts of a release of microsoft/go. The subcommands implement the steps.
`

// subcommands is the list of subcommand options, populated by each file's init function.
var subcommands []subcmd.Option

func main() {
	if err := subcmd.Run("releasego", description, subcommands); err != nil {
		log.Fatal(err)
	}
}

func tagFlag() *string {
	return flag.String("tag", "", "[Required] The tag name.")
}

// versionBranch determines the upstream branch that a given release version belongs to.
func versionBranch(v *goversion.GoVersion) string {
	branchBase := "release-branch.go"
	if v.Note == "fips" {
		branchBase = "dev.boringcrypto.go"
	}
	return branchBase + v.MajorMinor()
}

// appendPathAndVerificationFilePaths appends to p the path and the verification file (hash,
// signature) paths that should be available along with the file at path, assuming all of these
// files share a common URL prefix. This can be used to calculate what URLs should be available for
// a given build artifact URL.
func appendPathAndVerificationFilePaths(p []string, path string) []string {
	p = append(p, path, path+".sha256")
	if strings.HasSuffix(path, ".tar.gz") {
		p = append(p, path+".sig")
	}
	return p
}

// appendReleaseURLs appends to urls all URLs associated with the release of a specific set of build
// assets, along with the URL of the assets JSON file itself, and returns the result.
//
// If assetJSONUrl is empty, the asset JSON URL is determined by a filename convention that assumes
// the whole release is available with the same URL prefix on every asset.
func appendReleaseURLs(urls []string, assets *buildassets.BuildAssets, assetJSONUrl string) []string {
	var addedSrc bool
	for _, a := range assets.Arches {
		if a.Env == nil {
			addedSrc = true
		}

		urls = append(urls, a.URL)

		if a.SHA256ChecksumURL == "" {
			urls = append(urls, a.URL+".sha256")
		} else {
			urls = append(urls, a.SHA256ChecksumURL)
		}

		if a.PGPSignatureURL == "" {
			if strings.HasSuffix(a.URL, ".tar.gz") {
				urls = append(urls, a.URL+".sig")
			}
		} else {
			urls = append(urls, a.PGPSignatureURL)
		}
	}
	if !addedSrc {
		// If there wasn't a source archive in the "arches" section, apply a URL suffix convention.
		urls = appendPathAndVerificationFilePaths(urls, assets.GoSrcURL)
	}

	if assetJSONUrl == "" {
		// If an assets.json URL isn't specified, it's in the same virtual dir as src.
		goSrcURLParts := strings.Split(assets.GoSrcURL, "/")
		urlBase := strings.Join(goSrcURLParts[:len(goSrcURLParts)-1], "/") + "/"
		urls = append(urls, urlBase+"assets.json")
	} else {
		urls = append(urls, assetJSONUrl)
	}
	return urls
}
