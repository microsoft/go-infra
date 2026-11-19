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
	flag.Func("src", "source directory (repeatable; default: src)", func(dir string) error {
		rule.SourceRoots = append(rule.SourceRoots, dir)
		return nil
	})
	flag.Func("import", "import path to match, including subpackages (repeatable; default: crypto/internal/fips140)", func(path string) error {
		rule.ImportRoots = append(rule.ImportRoots, path)
		return nil
	})
	flag.StringVar(&rule.ExcludeDir, "exclude", "", "source directory to exclude")
	flag.Parse()
	if flag.NArg() != 2 {
		log.Fatal("usage: importdiff [flags] OLD NEW")
	}
	if len(rule.SourceRoots) == 0 {
		rule.SourceRoots = []string{"src"}
	}
	if len(rule.ImportRoots) == 0 {
		rule.ImportRoots = []string{"crypto/internal/fips140"}
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
