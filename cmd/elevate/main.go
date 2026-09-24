// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

// Elevate requests just-in-time administrator access to a GitHub repository
// through Microsoft's Open Source Management Portal.
package main

import (
	"bufio"
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"
)

const (
	commandName       = "elevate"
	defaultTimeout    = 15 * time.Minute
	maxDescriptionLen = 4096
)

type cliOptions struct {
	Repository string
	Browser    string
	ProfileDir string
	Timeout    time.Duration
}

type elevationRequest struct {
	Repository  repository
	Description string
}

type browserOptions struct {
	Executable string
	ProfileDir string
}

type cliDependencies struct {
	getwd   func() (string, error)
	detect  func(context.Context, string) (repository, error)
	elevate func(context.Context, elevationRequest, browserOptions) error
}

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	deps := cliDependencies{
		getwd:  os.Getwd,
		detect: detectGitHubRepository,
		elevate: func(ctx context.Context, request elevationRequest, options browserOptions) error {
			return newBrowserElevator(options, os.Stdout).elevate(ctx, request)
		},
	}
	if err := runCLI(ctx, os.Args[1:], os.Stdin, os.Stdout, os.Stderr, deps); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return
		}
		fmt.Fprintf(os.Stderr, "%s: %v\n", commandName, err)
		os.Exit(1)
	}
}

func runCLI(
	ctx context.Context,
	args []string,
	stdin io.Reader,
	stdout io.Writer,
	stderr io.Writer,
	deps cliDependencies,
) error {
	options, err := parseFlags(args, stderr)
	if err != nil {
		return err
	}

	var target repository
	if options.Repository != "" {
		target, err = parseRepository(options.Repository)
		if err != nil {
			return fmt.Errorf("parse -repo: %w", err)
		}
	} else {
		workingDirectory, err := deps.getwd()
		if err != nil {
			return fmt.Errorf("determine current directory: %w", err)
		}
		target, err = deps.detect(ctx, workingDirectory)
		if err != nil {
			return err
		}
	}

	reader := bufio.NewReaderSize(stdin, maxDescriptionLen+1)
	fmt.Fprint(stdout, "Brief description for the JIT elevation: ")
	description, err := readLine(reader)
	if err != nil {
		return fmt.Errorf("read description: %w", err)
	}
	description = strings.TrimSpace(description)
	if description == "" {
		return errors.New("description must not be empty")
	}

	portalURL := portalGrantURL(target)
	fmt.Fprintf(stdout, "\nRepository:  %s\n", target)
	fmt.Fprintf(stdout, "Portal URL:  %s\n", portalURL)
	fmt.Fprintf(stdout, "Description: %q\n\n", description)
	fmt.Fprint(stdout, "Request JIT administrator elevation? [y/N]: ")
	answer, err := readLine(reader)
	if err != nil {
		return fmt.Errorf("read confirmation: %w", err)
	}
	if !isConfirmation(answer) {
		fmt.Fprintln(stdout, "Cancelled; no elevation was requested.")
		return nil
	}

	requestCtx, cancel := context.WithTimeout(ctx, options.Timeout)
	defer cancel()
	err = deps.elevate(requestCtx, elevationRequest{
		Repository:  target,
		Description: description,
	}, browserOptions{
		Executable: options.Browser,
		ProfileDir: options.ProfileDir,
	})
	if err != nil {
		return fmt.Errorf("request elevation for %s: %w", target, err)
	}
	fmt.Fprintf(stdout, "JIT administrator elevation granted for %s.\n", target)
	return nil
}

func parseFlags(args []string, stderr io.Writer) (cliOptions, error) {
	options := cliOptions{Timeout: defaultTimeout}
	fs := flag.NewFlagSet(commandName, flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.StringVar(&options.Repository, "repo", "", "GitHub repository in OWNER/REPO form; defaults to the current Git repository")
	fs.StringVar(&options.Browser, "browser", "", "Path to Chrome, Edge, or Chromium; defaults to an installed browser")
	fs.StringVar(&options.ProfileDir, "profile-dir", "", "Browser profile directory; defaults to an OS user cache directory")
	fs.DurationVar(&options.Timeout, "timeout", defaultTimeout, "Overall timeout, including interactive browser sign-in")
	fs.Usage = func() {
		fmt.Fprintf(fs.Output(), "Usage: %s [-repo OWNER/REPO]\n\n", fs.Name())
		fmt.Fprintln(fs.Output(), "Requests temporary administrator access through the Microsoft OSS portal.")
		fmt.Fprintln(fs.Output(), "The command always prompts for a description and confirmation before submitting.")
		fmt.Fprintln(fs.Output(), "\nOptions:")
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		return cliOptions{}, err
	}
	if fs.NArg() != 0 {
		return cliOptions{}, fmt.Errorf("unexpected positional arguments: %s", strings.Join(fs.Args(), " "))
	}
	if options.Timeout <= 0 {
		return cliOptions{}, errors.New("-timeout must be greater than zero")
	}
	return options, nil
}

func readLine(reader *bufio.Reader) (string, error) {
	line, err := reader.ReadString('\n')
	if len(line) > maxDescriptionLen {
		return "", fmt.Errorf("input exceeds %d bytes", maxDescriptionLen)
	}
	if err != nil && !errors.Is(err, io.EOF) {
		return "", err
	}
	return strings.TrimRight(line, "\r\n"), nil
}

func isConfirmation(value string) bool {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "y", "yes":
		return true
	default:
		return false
	}
}
