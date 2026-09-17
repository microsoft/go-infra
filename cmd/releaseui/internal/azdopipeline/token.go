// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package azdopipeline

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"strings"
	"sync"
	"time"
)

// AzureDevOpsResourceID is the Microsoft Entra resource used by Azure DevOps.
const AzureDevOpsResourceID = "499b84ac-1321-427f-aa17-267ca6975798"

// CommandRunner runs a non-interactive local command and returns stdout.
type CommandRunner interface {
	Output(context.Context, string, ...string) ([]byte, error)
}

// ExecCommandRunner uses os/exec without logging arguments or output.
type ExecCommandRunner struct{}

func (ExecCommandRunner) Output(ctx context.Context, name string, args ...string) ([]byte, error) {
	return exec.CommandContext(ctx, name, args...).Output()
}

// AzureCLITokenProvider acquires an Azure DevOps bearer token from an existing az login.
type AzureCLITokenProvider struct {
	Runner CommandRunner
}

// Token implements TokenProvider. The token is returned only to the HTTP client and is never
// included in errors.
func (p AzureCLITokenProvider) Token(ctx context.Context) (string, error) {
	if p.Runner == nil {
		return "", errors.New("azure CLI command runner is nil")
	}
	output, err := p.Runner.Output(
		ctx,
		"az",
		"account", "get-access-token",
		"--resource", AzureDevOpsResourceID,
		"--query", "accessToken",
		"--output", "tsv",
	)
	if err != nil {
		return "", fmt.Errorf("azure CLI failed to acquire an Azure DevOps token: %w", err)
	}
	token := strings.TrimSpace(string(output))
	if token == "" {
		return "", errors.New("azure CLI returned an empty Azure DevOps token")
	}
	return token, nil
}

var _ TokenProvider = AzureCLITokenProvider{}

// CachingTokenProvider reuses a token for a short interval. This avoids running Azure CLI for
// every read while still refreshing well before a normal Azure DevOps access token expires.
type CachingTokenProvider struct {
	Provider TokenProvider
	TTL      time.Duration

	mu       sync.Mutex
	token    string
	acquired time.Time
}

// Token implements TokenProvider.
func (p *CachingTokenProvider) Token(ctx context.Context) (string, error) {
	if p == nil || p.Provider == nil {
		return "", errors.New("cached token provider is nil")
	}
	if p.TTL <= 0 {
		return "", errors.New("cached token TTL must be positive")
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.token != "" && time.Since(p.acquired) < p.TTL {
		return p.token, nil
	}
	token, err := p.Provider.Token(ctx)
	if err != nil {
		return "", err
	}
	p.token = token
	p.acquired = time.Now()
	return token, nil
}

var _ TokenProvider = (*CachingTokenProvider)(nil)
