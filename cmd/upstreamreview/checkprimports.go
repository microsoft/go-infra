// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"log"
	"os"
	"strconv"
	"time"

	"github.com/google/go-github/v92/github"
	"github.com/microsoft/go-infra/githubutil"
)

func checkPRImports(args []string) error {
	flags := flag.NewFlagSet("upstreamreview check-pr-imports", flag.ContinueOnError)
	repo := flags.String("repo", "", "target GitHub repository (owner/name)")
	n := flags.Int("pr", 0, "open pull request number")
	merge := flags.String("merge", "", "test merge commit for this workflow run")
	var p policy
	flags.StringVar(&p.upstream, "upstream", "", "upstream GitHub repository (owner/name)")
	flags.StringVar(&p.submodule, "submodule", "", "submodule path in the target repository")
	flags.StringVar(&p.label, "label", "", "PR label that acknowledges matching changes")
	p.rule.BindFlags(flags)
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 0 {
		return fmt.Errorf("unexpected arguments: %q", flags.Args())
	}
	if *n <= 0 || !objectID.MatchString(*merge) {
		return errors.New("a positive PR number and a full test merge SHA are required")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	client, err := githubutil.NewClient(ctx, os.Getenv("GH_TOKEN"))
	if err != nil {
		return err
	}
	g, err := newGate(client, *repo, p)
	if err != nil {
		return err
	}
	var event *github.PullRequestEvent
	if path := os.Getenv("GITHUB_EVENT_PATH"); path != "" {
		event, err = readPREvent(path)
		if err != nil {
			return err
		}
	}
	r, err := g.check(ctx, *n, *merge, event)
	if _, writeErr := fmt.Print(r.summary); writeErr != nil {
		return writeErr
	}
	if outputErr := r.writeOutput(os.Getenv("GITHUB_OUTPUT"), os.Getenv("GITHUB_RUN_ATTEMPT")); outputErr != nil {
		log.Printf("could not write optional review output: %v", outputErr)
	}
	return err
}

func (r report) writeOutput(path, attempt string) error {
	if path == "" || r.scan.Verdict == "" { // Outside Actions, or no verified result.
		return nil
	}
	n, err := strconv.Atoi(attempt)
	if err != nil || n <= 0 {
		return errors.New("GITHUB_RUN_ATTEMPT must be a positive number")
	}
	r.scan.Attempt = n
	data, err := json.Marshal(r.scan)
	if err != nil {
		return err
	}
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		return err
	}
	_, err = fmt.Fprintf(f, "review=%s\nupdate-comment=%t\n", data, r.updateComment)
	return errors.Join(err, f.Close())
}
