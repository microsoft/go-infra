// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package main

import (
	"flag"
	"regexp"

	"github.com/microsoft/go-infra/subcmd"
)

func init() {
	subcommands = append(subcommands, subcmd.Option{
		Name:        "update-azure-linux",
		Summary:     "Update the Azure Linux Microsoft Go version after release.",
		Description: "",
		Handle:      updateAzureLinux,
	})
}

func updateAzureLinux(p subcmd.ParseFunc) error {
	var buildAssetJSON string

	flag.StringVar(&buildAssetJSON, "build-asset-json", "", "[Required] The path of a build asset JSON file describing the Go build to update to.")

	if err := p(); err != nil {
		return err
	}

	return nil
}

const (
	golangSignaturesFilepath = "SPECS/golang/golang.signatures.json"
	golangSpecFilepath       = "SPECS/golang/golang.spec"
	cgManifestJSON           = "cgmanifest.json"
)

func updateSignaturesFile(signatureFileContent []byte, msGoFilename, msGoRevision, version string) []byte {
	content := string(signatureFileContent)

	// Define the regex patterns
	msGoFilenamePattern := regexp.MustCompile(`(%global ms_go_filename\s+)\S+`)
	msGoRevisionPattern := regexp.MustCompile(`(%global ms_go_revision\s+)\d+`)
	versionPattern := regexp.MustCompile(`(Version:\s+)\d+\.\d+\.\d+`)

	// Replace the matched patterns with the new values
	content = msGoFilenamePattern.ReplaceAllString(content, `${1}`+msGoFilename)
	content = msGoRevisionPattern.ReplaceAllString(content, `${1}`+msGoRevision)
	content = versionPattern.ReplaceAllString(content, `${1}`+version)

	return []byte(content)
}

func updateSpecFile() ([]byte, error) {

	return nil, nil
}

func updateCGManifest() ([]byte, error) {

	return nil, nil
}
