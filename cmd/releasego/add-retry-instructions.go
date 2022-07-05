// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package main

import (
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"

	"github.com/microsoft/go-infra/azdo"
	"github.com/microsoft/go-infra/subcmd"
)

func init() {
	subcommands = append(subcommands, subcmd.Option{
		Name:    "add-retry-instructions",
		Summary: "Create a Markdown doc with retry instructions and add it to the AzDO build.",
		Description: `

This command creates a temp markdown file and uses the 'task.uploadsummary' logging command to
upload and attach it to the currently running build. Scans the environment for polling variables
passed to the agent by AzDO that should be used to retry the currently running release build.

The temp markdown file is not cleaned up, to allow AzDO time to process the logging command.
`,
		Handle: handleAddRetryInstructions,
	})
}

func handleAddRetryInstructions(p subcmd.ParseFunc) error {
	checkboxes := flag.Bool("checkboxes", false, "Alert the dev to release checkboxes that may also need tweaking.")
	preapproval := flag.Bool("preapproval", false, "Alert the dev to a 'pre-approval' checkbox they should check.")

	if err := p(); err != nil {
		return err
	}

	// Match env vars like "poll1buildId=123" with the number and value in groups. If there are
	// multiple "=", treat the leftmost one as the separator. Ignore case.
	reg := regexp.MustCompile(`(?i)poll(\d+).+?=(.*)`)

	var lastNonNil string
	var lastNonNilIndex int
	for _, env := range os.Environ() {
		matches := reg.FindStringSubmatch(env)
		if len(matches) > 0 {
			fmt.Printf("Found polling env var %q. Matches: %#v\n", env, matches)
			num, err := strconv.Atoi(matches[1])
			if err != nil {
				return fmt.Errorf("match 1 is not an int: %w", err)
			}
			value := matches[2]

			if value != "nil" && num > lastNonNilIndex {
				lastNonNil = value
				lastNonNilIndex = num
			}
		}
	}

	var b strings.Builder

	// The AzDO Markdown renderer being used in this particular context (the Extensions page)
	// displays numbered lists as a bulleted list, and doesn't indent the code block after the first
	// step. Create our own numbering text instead.
	n := 1
	startNextEntry := func() {
		b.WriteString(strconv.Itoa(n))
		b.WriteString(" - ")
		n++
	}

	b.WriteString("# Retry Instructions\n\n")
	// If we found a non-nil polling parameter, tell the dev how to retry from that point. If we
	// didn't find any non-nil variables, the job probably timed out on the very first polling step,
	// so the dev just needs to requeue from the beginning.
	if lastNonNil != "" {
		startNextEntry()
		b.WriteString("Copy this value:\n\n")
		// Indenting this line would make it line up more nicely with the numbering scheme. However,
		// adding spaces makes the renderer show literal '*' and '`' rather than applying them as
		// formatting, so we don't do that.
		b.WriteString("**`")
		b.WriteString(lastNonNil)
		b.WriteString("`**\n\n")
	}
	startNextEntry()
	b.WriteString("Press **Run new**\n\n")
	if lastNonNil != "" {
		startNextEntry()
		b.WriteString("Paste the number into the field numbered **")
		b.WriteString(strconv.Itoa(lastNonNilIndex))
		b.WriteString("**\n\n")
	}
	if *checkboxes {
		startNextEntry()
		b.WriteString("If the build has successfully run some release steps (Git Tag, etc.), uncheck the corresponding boxes\n\n")
	}
	if *preapproval {
		startNextEntry()
		b.WriteString("If not already enabled, check the 'Approve right now' checkbox\n\n")
	}
	startNextEntry()
	b.WriteString("Press **Run**\n\n")

	content := b.String()

	fmt.Printf("Saving generated markdown text to file:\n%v\n", content)

	temp, err := os.CreateTemp(os.TempDir(), "retry-instructions-*.md")
	if err != nil {
		return err
	}
	defer temp.Close()

	if _, err := io.WriteString(temp, content); err != nil {
		return err
	}

	tempPath, err := filepath.Abs(temp.Name())
	if err != nil {
		return err
	}
	azdo.UploadBuildSummary(tempPath)

	return nil
}
