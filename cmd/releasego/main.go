// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package main

import (
	"flag"
	"log"
	"strings"

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
// signature) paths that should be available along with the file at path. This can be used to
// calculate what URLs should be available for a given build artifact URL.
func appendPathAndVerificationFilePaths(p []string, path string) []string {
	p = append(p, path, path+".sha256")
	if strings.HasSuffix(path, ".tar.gz") {
		p = append(p, path+".sig")
	}
	return p
}
