// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

// Command importdiff reports upstream source changes requiring import review.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"log"
	"os"

	"github.com/microsoft/go-infra/importdiff"
)

func main() {
	repo := flag.String("repo", ".", "Git repository containing both revisions")
	rule := importdiff.Rule{}
	flag.StringVar(&rule.SourceRoot, "src", "src", "source directory")
	flag.StringVar(&rule.ImportRoot, "import", "crypto/internal/fips140", "import path to match, including subpackages")
	flag.StringVar(&rule.ExcludeDir, "exclude", "", "source directory to exclude")
	flag.Parse()
	if flag.NArg() != 2 {
		log.Fatal("usage: importdiff [flags] OLD NEW")
	}
	changes, err := importdiff.Compare(context.Background(), *repo, flag.Arg(0), flag.Arg(1), rule)
	if err != nil {
		log.Fatal(err)
	}
	if changes == nil {
		changes = []importdiff.Change{}
	}
	if err := json.NewEncoder(os.Stdout).Encode(changes); err != nil {
		log.Fatal(err)
	}
}
