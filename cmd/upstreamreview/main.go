// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

// Command upstreamreview finds changes to upstream Go importers and reports them on pull requests.
package main

import (
	"errors"
	"flag"
	"fmt"
	"log"
	"os"
)

const usage = `usage: upstreamreview <command> [arguments]

  importdiff  List changed Go importers between two local Git revisions as JSON
  ghreview    Check a GitHub PR's submodule update and print a Markdown summary
  comment     Link to the workflow run summary in a single PR comment`

func main() {
	if err := run(os.Args[1:]); err != nil && !errors.Is(err, flag.ErrHelp) {
		log.Fatal(err)
	}
}

func run(args []string) error {
	if len(args) == 0 {
		return errors.New(usage)
	}
	switch args[0] {
	case "importdiff":
		return importDiff(args[1:])
	case "ghreview":
		return ghReview(args[1:])
	case "comment":
		return comment(args[1:])
	case "-h", "-help", "--help", "help":
		fmt.Fprintln(os.Stderr, usage)
		return nil
	default:
		return fmt.Errorf("unknown command %q\n%s", args[0], usage)
	}
}
