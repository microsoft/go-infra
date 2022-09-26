// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package main

import (
	_ "embed"
	"errors"
	"flag"
	"fmt"
	"html/template"
	"io"
	"log"
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

type retryTemplateArgs struct {
	Checkboxes    bool
	Preapproval   bool
	LastNonNilEnv *envArg
}

//go:embed templates/retry.template.md
var retryTemplate string

func handleAddRetryInstructions(p subcmd.ParseFunc) error {
	var args retryTemplateArgs
	flag.BoolVar(&args.Checkboxes, "checkboxes", false, "Alert the dev to release checkboxes that may also need tweaking.")
	flag.BoolVar(&args.Preapproval, "preapproval", false, "Alert the dev to a 'pre-approval' checkbox they should check.")

	if err := p(); err != nil {
		return err
	}

	for _, env := range os.Environ() {
		a, err := newEnvArg(env)
		if err != nil {
			return err
		}
		if a == nil || a.Value == "nil" {
			continue
		}
		if args.LastNonNilEnv == nil || args.LastNonNilEnv.Index < a.Index {
			args.LastNonNilEnv = a
		}
	}

	content, err := generateContent(args)
	if err != nil {
		log.Printf("Failed to evaluate template to produce instructions: %v\n", err)
		content = fmt.Sprintf(
			"Instruction text generation failed!\n\n"+
				"Args: %#v\n\n"+
				"Error: %v\n\n"+
				"[The instructions template.](https://github.com/microsoft/go-infra/tree/main/cmd/releasego/templates)\n",
			args,
			err)
	}

	fmt.Printf("Saving generated markdown text to file:\n===\n%v===\n", content)

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
	azdo.LogCmdUploadSummary(tempPath)

	return nil
}

// Match env vars like "poll1buildId=123" with the number and value in groups. If there are
// multiple "=", treat the leftmost one as the separator. Ignore case.
var reg = regexp.MustCompile(`(?i)poll(\d+)(.+?)=(.*)`)

type envArg struct {
	Value string
	Name  string
	Index int
}

func newEnvArg(envVar string) (*envArg, error) {
	matches := reg.FindStringSubmatch(envVar)
	if len(matches) > 0 {
		fmt.Printf("Found polling env var %q. Matches: %#v\n", envVar, matches)
		num, err := strconv.Atoi(matches[1])
		if err != nil {
			return nil, fmt.Errorf("match 1 is not an int: %w", err)
		}
		value := matches[3]

		return &envArg{value, matches[2], num}, nil
	}
	return nil, nil
}

func generateContent(args retryTemplateArgs) (string, error) {
	// The AzDO Markdown renderer being used in this particular context (the Extensions page)
	// displays numbered lists as a bulleted list, and doesn't indent the code block after the first
	// step. Create our own numbering text instead.
	n := 0
	funcs := template.FuncMap{
		"nextListEntry": func() string {
			n++
			return strconv.Itoa(n) + " - "
		},
		// AzDO capitalizes env vars, which makes them less readable, but the template has to
		// compare against them to determine which instructions to show. Let the template use the
		// readable mixed-case string by providing a case-insensitive "ieq" comparison func that
		// behaves like "eq".
		"ieq": func(args ...string) (bool, error) {
			if len(args) < 2 {
				return false, errors.New("missing argument for comparison")
			}
			left := args[0]
			for _, right := range args[1:] {
				if strings.EqualFold(left, right) {
					return true, nil
				}
			}
			return false, nil
		},
	}

	t, err := template.New("retry.template.md").Funcs(funcs).Parse(retryTemplate)
	if err != nil {
		return "", err
	}

	var b strings.Builder
	if err := t.Execute(&b, args); err != nil {
		return "", err
	}

	return b.String(), nil
}
