// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package main

import (
	"errors"
	"flag"
	"fmt"
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
		Summary: "Wait (poll) for the latest Microsoft Go images on MAR to match specified versions.",
		Description: `

Given a list of Go versions, constructs tag names for each one's major version (1.20.3-1 -> 1.20)
and repeatedly tries to "docker pull" that tag and then probe the image using
"docker run ... go version" to see if it contains the expected major+minor+patch Go version.
`,
		Handle: waitMarGoVersion,
	})
}

func waitMarGoVersion(p subcmd.ParseFunc) error {
	versionList := flag.String(
		"versions", "",
		"[Required] A list of full or partial microsoft/go version numbers (major.minor.patch[-revision[-suffix]]). Separated by commas.")

	timeout := flag.Duration("timeout", time.Minute*5, "Time to wait before giving up.")
	pollDelay := flag.Duration("poll-delay", time.Second*10, "Time to wait between each poll attempt.")

	if err := p(); err != nil {
		return err
	}

	if *versionList == "" {
		return errors.New("no versions specified")
	}

	var checkers []func() (bool, error)
	for _, version := range strings.Split(*versionList, ",") {
		v := goversion.New(version)
		tag := marRepo + ":" + v.MajorMinor()

		expect := "go" + v.MajorMinorPatchPrerelease() + " "

		checkers = append(checkers, func() (bool, error) {
			pullCmd := exec.Command("docker", "pull", tag)
			if err := executil.Run(pullCmd); err != nil {
				return false, err
			}
			versionCmd := exec.Command("docker", "run", "--rm", tag, "go", "version")
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

	start := time.Now()
	end := start.Add(*timeout)

	for {
		var missing bool

		for _, tag := range checkers {
			ok, err := tag()
			if err != nil {
				return fmt.Errorf("unexpectedly failed check: %v", err)
			}
			if !ok {
				missing = true
			}
		}

		if !missing {
			log.Printf("All checkers found what they expected. Done.")
			return nil
		}

		log.Printf("Waiting %v before trying again...", *pollDelay)
		time.Sleep(*pollDelay)
		if time.Now().After(end) {
			return fmt.Errorf("exceeded timeout (%v) waiting for versions", *timeout)
		}
	}
}
