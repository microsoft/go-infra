// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package main

import (
	"errors"
	"flag"
	"fmt"
	"go/version"
	"log"
	"os/exec"
	"strings"
	"time"

	"github.com/microsoft/go-infra/executil"
	"github.com/microsoft/go-infra/goversion"
	"github.com/microsoft/go-infra/subcmd"
)

const marRepo = "mcr.microsoft.com/oss/go/microsoft/golang"

func init() {
	subcommands = append(subcommands, subcmd.Option{
		Name:    "wait-latest-mar-go-version",
		Summary: "Wait (poll) for the latest Microsoft build of Go images on MAR to match specified versions.",
		Description: `

Given a list of Go versions, constructs tag names for each one's major version (1.20.3-1 -> 1.20)
and repeatedly tries to "docker pull" that tag and then probe the image using
"docker run ... go version" to see if it contains the expected major+minor+patch Go version.
For Go 1.26.1 and later, also checks the Microsoft revision using GODEBUG=ms_version=1.
`,
		Handle: waitMarGoVersion,
	})
}

func waitMarGoVersion(p subcmd.ParseFunc) error {
	versionList := flag.String(
		"versions", "",
		"[Required] A list of full or partial microsoft/go version numbers (major.minor.patch[-revision[-suffix]]). Separated by commas.")

	timeout := flag.Duration("timeout", 5*time.Minute, "Time to wait before giving up.")
	pollDelay := flag.Duration("poll-delay", 10*time.Second, "Time to wait between each poll attempt.")

	if err := p(); err != nil {
		return err
	}

	if *versionList == "" {
		return errors.New("no versions specified")
	}

	var checkers []func() (bool, error)
	for version := range strings.SplitSeq(*versionList, ",") {
		v := goversion.New(version)
		tag := marRepo + ":" + v.MajorMinor()

		expect, checkRevision := expectedMarGoVersion(v)

		checkers = append(checkers, func() (bool, error) {
			pullCmd := exec.Command("docker", "pull", tag)
			if err := executil.Run(pullCmd); err != nil {
				return false, err
			}
			args := []string{"run", "--rm"}
			if checkRevision {
				args = append(args, "--env", "GODEBUG=ms_version=1")
			}
			args = append(args, tag, "go", "version")
			versionCmd := exec.Command("docker", args...)
			out, err := executil.SpaceTrimmedCombinedOutput(versionCmd)
			if err != nil {
				return false, err
			}
			found := strings.Contains(out, expect)
			log.Printf("Finding %q in %q: %v\n", expect, out, found)
			return found, nil
		})
	}

	// Make our logs stand out from Docker's.
	log.Default().SetPrefix("---- ")

	end := time.Now().Add(*timeout)

	for time.Now().Before(end) {
		var missing bool

		for _, checker := range checkers {
			ok, err := checker()
			if err != nil {
				return fmt.Errorf("unexpectedly failed check: %v", err)
			}
			if !ok {
				missing = true
				// When a checker doesn't find what it's looking for, keep running the remaining
				// checkers. If this command times out, a dev will read the output to figure out
				// what's happening. It may help them to see the status of all tags rather than
				// just the first missing one.
			}
		}

		if !missing {
			log.Printf("All checkers found what they expected. Done.")
			return nil
		}

		log.Printf("Waiting %v before trying again...", *pollDelay)
		time.Sleep(*pollDelay)
	}
	return fmt.Errorf("exceeded timeout (%v) waiting for versions", *timeout)
}

func expectedMarGoVersion(v *goversion.GoVersion) (expected string, checkRevision bool) {
	// Go 1.26.0-1 does not reliably include the Microsoft revision in its version string.
	if version.Compare("go"+v.MajorMinorPatch(), "go1.26.1") >= 0 {
		return "go" + v.MajorMinorPatchPrereleaseRevision() + "-microsoft ", true
	}
	return "go" + v.MajorMinorPatchPrerelease() + " ", false
}
