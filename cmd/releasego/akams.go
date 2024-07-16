// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path"
	"text/tabwriter"

	"github.com/microsoft/go-infra/buildmodel/buildassets"
	"github.com/microsoft/go-infra/buildmodel/publishmanifest"
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

Either clientSecret or clientCertVaultFile must be passed to authenticate with the AKA.MS API.

Example:

  go run ./cmd/releasego akams -build-asset-json /downloads/assets.json -version 1.17.8-1
`,
		Handle: handleAKAMS,
	})
}

func handleAKAMS(p subcmd.ParseFunc) error {
	buildAssetJSON := flag.String("build-asset-json", "", "[Required] The path of a build asset JSON file describing the Go build to update to.")

	buildAssetJSONPublishManifest := flag.String(
		"build-asset-json-publish-manifest", "",
		"The path of a publish manifest describing where the build asset JSON file is available.")

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
		&akaMSClientCertVaultFile,
		"clientCertVaultFile", "",
		"The path of the certificate to use for the AKA.MS API. "+
			"The expected format matches the output of 'az keyvault secret show': JSON with a property 'value' that contains a base64-encoded PFX-encoded certificate.")
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

	if err := createAkaMSLinks(*buildAssetJSON, *buildAssetJSONPublishManifest); err != nil {
		log.Fatalf("error: %v\n", err)
	}
	return nil
}

var (
	latestShortLinkPrefix    string
	akaMSClientID            string
	akaMSClientSecret        string
	akaMSClientCertVaultFile string
	akaMSTenant              string
	akaMSCreatedBy           string
	akaMSGroupOwner          string
	akaMSOwners              string
)

func createAkaMSLinks(assetFilePath, assetManifestPath string) error {
	ctx := context.Background()

	var b buildassets.BuildAssets
	if err := stringutil.ReadJSONFile(assetFilePath, &b); err != nil {
		return err
	}

	var assetJSONUrl string
	if assetManifestPath != "" {
		var m publishmanifest.Manifest
		if err := stringutil.ReadJSONFile(assetManifestPath, &m); err != nil {
			return fmt.Errorf("failed to read publish manifest: %v", err)
		}
		for _, p := range m.Published {
			if p.Filename == "assets.json" {
				assetJSONUrl = p.URL
				break
			}
		}
	}

	linkPairs, err := createLinkPairs(b, assetJSONUrl)
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
	log.Println("---- Summary")
	if err := writeLinkPairTable(os.Stdout, linkPairs); err != nil {
		return err
	}
	log.Println("----")

	var transport *msal.ConfidentialCredentialTransport
	// Prefer certificate if multiple authentication methods are provided.
	if akaMSClientCertVaultFile != "" {
		jsonBytes, err := os.ReadFile(akaMSClientCertVaultFile)
		if err != nil {
			return fmt.Errorf("failed to read Azure Key Vault file: %v", err)
		}
		transport, err = msal.NewConfidentialTransportFromAzureKeyVaultJSON(msal.MicrosoftAuthority, akaMSClientID, jsonBytes)
		if err != nil {
			return fmt.Errorf("failed to create MSAL transport with Azure Key Vault certificate: %v", err)
		}
	} else if akaMSClientSecret != "" {
		transport, err = msal.NewConfidentialTransportFromSecret(msal.MicrosoftAuthority, akaMSClientID, akaMSClientSecret)
		if err != nil {
			return fmt.Errorf("failed to create MSAL transport with client secret: %v", err)
		}
	} else {
		return errors.New("no authentication details provided")
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

func createLinkPairs(assets buildassets.BuildAssets, assetJSONUrl string) ([]akaMSLinkPair, error) {
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

	// Get list of all URLs to link to.
	urls := appendReleaseURLs(nil, &assets, assetJSONUrl)

	pairs := make([]akaMSLinkPair, 0, len(urls)*len(partial))

	for _, p := range partial {
		for _, u := range urls {
			filename := path.Base(u)
			f, err := makeFloatingFilename(filename, p)
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

func writeLinkPairTable(w io.Writer, pairs []akaMSLinkPair) error {
	tw := tabwriter.NewWriter(w, 0, 0, 1, ' ', 0)
	for _, l := range pairs {
		fmt.Fprintf(tw, "%s\t->\t%s\n", l.Short, l.Target)
	}
	return tw.Flush()
}

func makeFloatingFilename(filename, floatVersion string) (string, error) {
	// The assets.json filename has no version number in it, so we need to add one.
	if filename == "assets.json" {
		return "go" + floatVersion + "." + filename, nil
	}

	_, platform, ext, ok := buildassets.CutToolsetFileParts(filename)
	if !ok {
		return "", fmt.Errorf("unable to find platform in filename %#q", filename)
	}

	// This is a tar.gz or zip of a Go build. To make a floating filename, the version information
	// needs to be removed, and the platform/target kept.
	return "go" + floatVersion + "." + platform + ext, nil
}
