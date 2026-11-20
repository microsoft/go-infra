// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"os"

	"github.com/microsoft/go-infra/internal/importdiff"
)

func importDiff(args []string) error {
	flags := flag.NewFlagSet("upstreamreview importdiff", flag.ContinueOnError)
	repo := flags.String("repo", ".", "Git repository containing both revisions")
	rule := importdiff.Rule{SourceRoots: []string{"src"}, ImportRoots: []string{"crypto/internal/fips140"}}
	rule.BindFlags(flags)
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 2 {
		return errors.New("usage: upstreamreview importdiff [flags] OLD NEW")
	}
	changes, err := importdiff.Compare(context.Background(), *repo, flags.Arg(0), flags.Arg(1), rule)
	if err != nil {
		return err
	}
	if changes == nil {
		changes = []importdiff.Change{}
	}
	return json.NewEncoder(os.Stdout).Encode(changes)
}
