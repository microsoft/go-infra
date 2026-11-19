// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

// Command upstreamreview checks changes to upstream Go importers in a PR's
// submodule update and prints a Markdown job summary.
// Run it from a checkout of the test merge that includes both parents.
package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os"
	"time"

	"github.com/microsoft/go-infra/githubutil"
)

func main() {
	repo := githubutil.BindRepoFlag()
	n := flag.Int("pr", 0, "open pull request number")
	merge := flag.String("merge", "", "test merge commit for this workflow run")
	var p policy
	flag.StringVar(&p.upstream, "upstream", "", "upstream GitHub repository (owner/name)")
	flag.StringVar(&p.submodule, "submodule", "", "submodule path in the target repository")
	flag.StringVar(&p.label, "label", "", "PR label that acknowledges matching changes")
	p.rule.BindFlags(flag.CommandLine)
	flag.Parse()
	if flag.NArg() != 0 {
		log.Fatalf("unexpected arguments: %q", flag.Args())
	}
	if *n <= 0 || !objectID.MatchString(*merge) {
		log.Fatal("a positive PR number and a full test merge SHA are required")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	client, err := githubutil.NewClient(ctx, os.Getenv("GH_TOKEN"))
	if err != nil {
		log.Fatal(err)
	}
	g, err := newGate(client, *repo, p)
	if err != nil {
		log.Fatal(err)
	}
	summary, err := g.check(ctx, *n, *merge)
	if _, writeErr := fmt.Print(summary); writeErr != nil {
		log.Fatal(writeErr)
	}
	if err != nil {
		log.Fatal(err)
	}
}
