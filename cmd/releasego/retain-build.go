// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package main

import (
	"flag"
	"log"
	"strconv"

	"github.com/microsoft/go-infra/azdo"
	"github.com/microsoft/go-infra/subcmd"
)

func init() {
	subcommands = append(subcommands, subcmd.Option{
		Name:    "retain-build",
		Summary: "Mark an AzDO build to be retained forever (set keepForever=true).",
		Description: `
Note: The build retention API is currently broken, so this command will not actually retain the build.
See https://github.com/microsoft/go-lab/issues/575

By default, retains the build that is currently running this command, using
BUILD_BUILDID, SYSTEM_COLLECTIONURI, and SYSTEM_TEAMPROJECT from the environment.
Pass -id, -org, or -proj to override.
`,
		Handle: handleRetainBuild,
	})
}

func handleRetainBuild(p subcmd.ParseFunc) error {
	id := flag.Int("id", envBuildID(), "The AzDO build ID to retain. Defaults to the current build (env BUILD_BUILDID).")
	azdoFlags := azdo.BindClientFlagsWithEnvDefaults()

	if err := p(); err != nil {
		return err
	}

	if *id == 0 {
		flag.Usage()
		log.Fatalln("No build ID specified and BUILD_BUILDID env var is not set.")
	}
	if err := azdoFlags.EnsureAssigned(); err != nil {
		flag.Usage()
		return err
	}

	log.Println("Build retention API broken; skipping to unblock release process. See https://github.com/microsoft/go-lab/issues/575")
	return nil
}

// envBuildID returns the AzDO BUILD_BUILDID env var as an int, or 0 if it is
// unset or unparseable. Used as the default for the -id flag so that -h shows
// the resolved value when running inside a pipeline.
func envBuildID() int {
	v := azdo.GetEnvBuildID()
	if v == "" {
		return 0
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		return 0
	}
	return n
}
