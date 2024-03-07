// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"net/http"
	"strings"

	"github.com/microsoft/go-infra/buildmodel/buildassets"
	"github.com/microsoft/go-infra/internal/akams"
	"github.com/microsoft/go-infra/internal/msal"
	"github.com/microsoft/go-infra/stringutil"
	"github.com/microsoft/go-infra/subcmd"
)

func init() {
	subcommands = append(subcommands, subcmd.Option{
		Name:    "akams",
		Summary: "Create aka.ms links based on a given build asset JSON file.",
		Description: `
Example:

  go run ./cmd/releasego akams -build-asset-json /downloads/assets.json -version 1.17.8-1
`,
		Handle: handleAKAMS,
	})
}

func handleAKAMS(p subcmd.ParseFunc) error {
	buildAssetJSON := flag.String("build-asset-json", "", "[Required] The path of a build asset JSON file describing the Go build to update to.")

	flag.StringVar(
		&latestShortLinkPrefix,
		"prefix", "golang/release/dev/latest/",
		"The shortened URL prefix to use, including '/'. The default value includes 'dev' and is not intended for production use.")
	flag.StringVar(
		&akaMSClientID,
		"clientID", "",
		"The client ID to use for the AKA.MS API.")
	flag.StringVar(
		&akaMSClientSecret,
		"clientSecret", "",
		"The client secret to use for the AKA.MS API.")
	flag.StringVar(
		&akaMSTenant,
		"tenant", "",
		"The tenant to use for the AKA.MS API.")
	flag.StringVar(
		&akaMSCreatedBy,
		"createdBy", "",
		"The user to use as the creator of the AKA.MS links.")
	flag.StringVar(
		&akaMSGroupOwner,
		"groupOwner", "",
		"The group owner to use for the AKA.MS links.")
	flag.StringVar(
		&akaMSOwners,
		"owners", "",
		"The owners to use for the AKA.MS links.")

	if err := p(); err != nil {
		return err
	}

	if *buildAssetJSON == "" {
		flag.Usage()
		log.Fatal("No build asset JSON specified.\n")
	}

	if err := createAkaMSLinks(*buildAssetJSON); err != nil {
		log.Fatalf("error: %v\n", err)
	}
	return nil
}

var (
	latestShortLinkPrefix string
	akaMSClientID         string
	akaMSClientSecret     string
	akaMSTenant           string
	akaMSCreatedBy        string
	akaMSGroupOwner       string
	akaMSOwners           string
)

func createAkaMSLinks(assetFilePath string) error {
	ctx := context.Background()

	var b buildassets.BuildAssets
	if err := stringutil.ReadJSONFile(assetFilePath, &b); err != nil {
		return err
	}

	linkPairs, err := createLinkPairs(b)
	if err != nil {
		return err
	}

	links := make([]akams.CreateLinkRequest, len(linkPairs))
	for i, l := range linkPairs {
		links[i] = akams.CreateLinkRequest{
			ShortURL:       l.Short,
			TargetURL:      l.Target,
			CreatedBy:      akaMSCreatedBy,
			LastModifiedBy: akaMSCreatedBy,
			Owners:         akaMSOwners,
			GroupOwner:     akaMSGroupOwner,
			IsVanity:       true,
			IsAllowParam:   true,
		}
	}
	payload, err := json.MarshalIndent(links, "", "  ")
	if err != nil {
		return err
	}
	log.Printf("---- Links %v\n", string(payload))

	transport, err := msal.NewConfidentialTransport(msal.MicrosoftAuthority, akaMSClientID, akaMSClientSecret)
	if err != nil {
		return fmt.Errorf("failed to create MSAL transport: %v", err)
	}
	transport.Scopes = []string{akams.Scope + "/.default"}
	client, err := akams.NewClient(akaMSTenant, &http.Client{Transport: transport})
	if err != nil {
		return fmt.Errorf("failed to create aka.ms client: %v", err)
	}

	if err := client.CreateOrUpdateBulk(ctx, links); err != nil {
		return fmt.Errorf("failed to create aka.ms bulk links: %v", err)
	}
	return nil
}

type akaMSLinkPair struct {
	Short  string
	Target string
}

func createLinkPairs(assets buildassets.BuildAssets) ([]akaMSLinkPair, error) {
	v := assets.GoVersion()
	// The partial versions that we want to link to a specific build.
	// For example, 1.18-fips -> 1.18.2-1-fips.
	partial := []string{
		v.MajorMinorPrerelease() + v.NoteWithPrefix(),
		v.MajorMinorPatchPrerelease() + v.NoteWithPrefix(),
		// Also include the fully specified version. This lets people use a pretty link even if they
		// do need to pin to a specific version.
		v.Full(),
	}

	goSrcURLParts := strings.Split(assets.GoSrcURL, "/")
	if len(goSrcURLParts) < 3 {
		return nil, fmt.Errorf("unable to determine build number from %#q: not enough '/' segments to be an asset URL", assets.GoSrcURL)
	}
	buildNumber := goSrcURLParts[len(goSrcURLParts)-2]

	urls := make([]string, 0, 3*(len(assets.Arches)+1))
	for _, a := range assets.Arches {
		urls = appendPathAndVerificationFilePaths(urls, a.URL)
	}
	urls = appendPathAndVerificationFilePaths(urls, assets.GoSrcURL)
	// The assets.json is uploaded in the same virtual dir as src.
	// Make an aka.ms URL for it.
	urlBase := strings.Join(goSrcURLParts[:len(goSrcURLParts)-1], "/") + "/"
	urls = append(urls, urlBase+"assets.json")

	pairs := make([]akaMSLinkPair, 0, len(urls)*len(partial))

	for _, p := range partial {
		for _, u := range urls {
			urlParts := strings.Split(u, "/")
			if len(urlParts) < 3 {
				return nil, fmt.Errorf("unable to determine short link for %#q: not enough '/' segments to be an asset URL", u)
			}
			filename := urlParts[len(urlParts)-1]
			f, err := makeFloatingFilename(filename, buildNumber, p)
			if err != nil {
				return nil, fmt.Errorf("unable to process URL %#q: %w", u, err)
			}

			pairs = append(pairs, akaMSLinkPair{
				Short:  latestShortLinkPrefix + f,
				Target: u,
			})
		}
	}

	return pairs, nil
}

func makeFloatingFilename(filename, buildNumber, floatVersion string) (string, error) {
	// The assets.json filename has no version number in it, so we need to add one.
	if filename == "assets.json" {
		return "go" + floatVersion + "." + filename, nil
	}
	// The build number and all information before it is version-related and needs to be replaced.
	if _, after, ok := strings.Cut(filename, buildNumber); ok {
		return "go" + floatVersion + after, nil
	}
	return "", fmt.Errorf("unable to find buildNumber %#q in filename %#q", buildNumber, filename)
}
