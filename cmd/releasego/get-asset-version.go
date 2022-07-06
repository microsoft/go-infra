// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package main

import (
	"flag"
	"fmt"
	"log"

	"github.com/microsoft/go-infra/azdo"
	"github.com/microsoft/go-infra/buildmodel/buildassets"
	"github.com/microsoft/go-infra/goversion"
	"github.com/microsoft/go-infra/stringutil"
	"github.com/microsoft/go-infra/subcmd"
)

func init() {
	subcommands = append(subcommands, subcmd.Option{
		Name:    "get-asset-version",
		Summary: "Gets the asset version out of a build asset JSON file.",
		Description: `

Log the asset version found in the given build asset JSON file. Optionally print an AzDO logging
command to set a variable that later release automation steps can use.

This command can also validate that the build asset JSON version matches an expected string version.
Exits 0 if the given version number matches the content of the build asset JSON file, or 1 if it
doesn't. This is used in release automation as a safety check.
`,
		Handle: handleAssetVersion,
	})
}

func handleAssetVersion(p subcmd.ParseFunc) error {
	buildAssetJSON := flag.String("build-asset-json", "", "[Required] The path of a build asset JSON file to read.")

	validateVersionFlag := flag.String(
		"version", "",
		"A Microsoft-built Go version, in 1.2.3-1[-fips] format.\n"+
			"If specified, must match the build asset JSON's version or this command will fail.\n"+
			"The string 'nil' is treated as the same as not setting the value, for use in CI where empty string can't be used.\n"+
			"Optionally use this to validate expectations.")

	setVariable := flag.String("set-azdo-variable", "", "An AzDO variable name to set.")

	if err := p(); err != nil {
		return err
	}

	if *buildAssetJSON == "" {
		flag.Usage()
		log.Fatal("No build asset JSON specified.\n")
	}
	if *validateVersionFlag == "nil" {
		*validateVersionFlag = ""
	}

	var b buildassets.BuildAssets
	if err := stringutil.ReadJSONFile(*buildAssetJSON, &b); err != nil {
		return err
	}

	assetVersion := b.GoVersion().Full()
	log.Printf("Found version: %v\n", assetVersion)
	if *setVariable != "" {
		azdo.LogCmdSetVariable(*setVariable, assetVersion)
	}

	if *validateVersionFlag != "" {
		inputVersion := goversion.New(*validateVersionFlag).Full()
		if assetVersion != inputVersion {
			return fmt.Errorf("build asset JSON version %q doesn't match input version %q", assetVersion, inputVersion)
		}
		log.Printf("Verified version is expected version.\n")
	}

	return nil
}
