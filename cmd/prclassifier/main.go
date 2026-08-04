// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

// prclassifier applies size, kind, and repository-defined area labels to GitHub
// pull requests.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
)

func main() {
	log.SetPrefix("prclassifier: ")
	log.SetFlags(0)

	cfg, err := parseConfig(os.Args[1:], os.Getenv, os.Stderr)
	if errors.Is(err, flag.ErrHelp) {
		return
	}
	if err != nil {
		log.Fatal(err)
	}

	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt)
	defer cancel()

	client, err := newGitHubAPI(ctx, cfg.Token)
	if err != nil {
		log.Fatalf("create GitHub client: %v", err)
	}
	if err := execute(ctx, cfg, client, os.Stdout); err != nil {
		fmt.Fprintf(os.Stderr, "prclassifier: %v\n", err)
		os.Exit(1)
	}
}
