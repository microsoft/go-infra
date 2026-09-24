// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package main

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"os/exec"
	"regexp"
	"sort"
	"strings"
)

var (
	ownerPattern      = regexp.MustCompile(`^[A-Za-z0-9](?:[A-Za-z0-9-]{0,37}[A-Za-z0-9])?$`)
	repositoryPattern = regexp.MustCompile(`^[A-Za-z0-9._-]{1,100}$`)
)

type repository struct {
	Owner string
	Name  string
}

func (r repository) String() string {
	return r.Owner + "/" + r.Name
}

type remoteCandidate struct {
	Remote     string
	Repository repository
}

func parseRepository(value string) (repository, error) {
	owner, name, ok := strings.Cut(strings.TrimSpace(value), "/")
	if !ok || strings.Contains(name, "/") {
		return repository{}, errors.New("repository must use OWNER/REPO form")
	}
	if !ownerPattern.MatchString(owner) {
		return repository{}, fmt.Errorf("invalid GitHub owner %q", owner)
	}
	if !repositoryPattern.MatchString(name) || name == "." || name == ".." {
		return repository{}, fmt.Errorf("invalid GitHub repository name %q", name)
	}
	return repository{Owner: owner, Name: name}, nil
}

func parseGitHubRemote(value string) (repository, bool) {
	value = strings.TrimSpace(value)
	if value == "" {
		return repository{}, false
	}

	var host, remotePath string
	if parsed, err := url.Parse(value); err == nil && parsed.Scheme != "" && parsed.Host != "" {
		host = parsed.Hostname()
		remotePath = parsed.Path
	} else {
		left, right, ok := strings.Cut(value, ":")
		if !ok || strings.Contains(left, "/") {
			return repository{}, false
		}
		host = left
		if _, after, ok := strings.Cut(host, "@"); ok {
			host = after
		}
		remotePath = right
	}

	host = strings.ToLower(host)
	if host != "github.com" && host != "ssh.github.com" {
		return repository{}, false
	}
	remotePath = strings.Trim(remotePath, "/")
	if host == "ssh.github.com" {
		remotePath = strings.TrimPrefix(remotePath, "v3/")
	}
	remotePath = strings.TrimSuffix(remotePath, ".git")
	target, err := parseRepository(remotePath)
	return target, err == nil
}

func detectGitHubRepository(ctx context.Context, workingDirectory string) (repository, error) {
	root, err := runGit(ctx, workingDirectory, "rev-parse", "--show-toplevel")
	if err != nil {
		return repository{}, fmt.Errorf("find Git repository: %w", err)
	}
	root = strings.TrimSpace(root)

	remoteOutput, err := runGit(ctx, root, "remote")
	if err != nil {
		return repository{}, fmt.Errorf("list Git remotes: %w", err)
	}
	var candidates []remoteCandidate
	for _, remote := range strings.Fields(remoteOutput) {
		urlOutput, err := runGit(ctx, root, "remote", "get-url", "--all", "--", remote)
		if err != nil {
			return repository{}, fmt.Errorf("read Git remote %q: %w", remote, err)
		}
		for _, remoteURL := range strings.Split(strings.TrimSpace(urlOutput), "\n") {
			if target, ok := parseGitHubRemote(remoteURL); ok {
				candidates = append(candidates, remoteCandidate{
					Remote:     remote,
					Repository: target,
				})
			}
		}
	}
	target, err := selectRepository(candidates)
	if err != nil {
		return repository{}, fmt.Errorf("%w; specify -repo OWNER/REPO", err)
	}
	return target, nil
}

func runGit(ctx context.Context, directory string, args ...string) (string, error) {
	commandArgs := append([]string{"-C", directory}, args...)
	command := exec.CommandContext(ctx, "git", commandArgs...)
	output, err := command.Output()
	if err != nil {
		var exitError *exec.ExitError
		if errors.As(err, &exitError) {
			message := strings.TrimSpace(string(exitError.Stderr))
			if message != "" {
				return "", fmt.Errorf("%w: %s", err, message)
			}
		}
		return "", err
	}
	return string(output), nil
}

func selectRepository(candidates []remoteCandidate) (repository, error) {
	if len(candidates) == 0 {
		return repository{}, errors.New("no github.com remote found")
	}

	bestPriority := 3
	selected := make(map[string]remoteCandidate)
	for _, candidate := range candidates {
		priority := remotePriority(candidate.Remote)
		if priority > bestPriority {
			continue
		}
		if priority < bestPriority {
			bestPriority = priority
			clear(selected)
		}
		key := strings.ToLower(candidate.Repository.String())
		selected[key] = candidate
	}
	if len(selected) == 1 {
		for _, candidate := range selected {
			return candidate.Repository, nil
		}
	}

	choices := make([]string, 0, len(selected))
	for _, candidate := range selected {
		choices = append(choices, fmt.Sprintf("%s (%s)", candidate.Repository, candidate.Remote))
	}
	sort.Strings(choices)
	return repository{}, fmt.Errorf("multiple GitHub remotes are equally preferred: %s", strings.Join(choices, ", "))
}

func remotePriority(remote string) int {
	switch strings.ToLower(remote) {
	case "upstream":
		return 0
	case "origin":
		return 1
	default:
		return 2
	}
}
