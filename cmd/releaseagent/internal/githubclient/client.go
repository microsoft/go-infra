// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

// Package githubclient provides reusable GitHub operations through an authenticated gh CLI.
package githubclient

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
	"strings"
	"time"
)

// CommandRunner executes gh without exposing its authentication token to callers.
type CommandRunner interface {
	Run(context.Context, []byte, ...string) ([]byte, error)
}

// ExecCommandRunner runs the locally authenticated gh executable.
type ExecCommandRunner struct{}

// Run executes gh with optional JSON stdin.
func (ExecCommandRunner) Run(ctx context.Context, input []byte, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, "gh", args...)
	if input != nil {
		cmd.Stdin = bytes.NewReader(input)
	}
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	output, err := cmd.Output()
	if err == nil {
		return output, nil
	}
	message := strings.TrimSpace(stderr.String())
	if message == "" {
		return nil, fmt.Errorf("gh %s: %w", firstArg(args), err)
	}
	return nil, fmt.Errorf("gh %s: %w: %s", firstArg(args), err, message)
}

func firstArg(args []string) string {
	if len(args) == 0 {
		return "command"
	}
	return args[0]
}

// Client performs GitHub operations through a fixed hostname.
type Client struct {
	host             string
	runner           CommandRunner
	sleep            func(context.Context, time.Duration) error
	newCorrelationID func() (string, error)
	discoveryPeriod  time.Duration
	pollInterval     time.Duration
}

// New creates a GitHub client for host.
func New(host string, runner CommandRunner) (*Client, error) {
	if strings.TrimSpace(host) == "" {
		return nil, errors.New("GitHub hostname is empty")
	}
	if runner == nil {
		return nil, errors.New("GitHub command runner is nil")
	}
	return &Client{
		host: host, runner: runner, sleep: sleep, newCorrelationID: newCorrelationID,
		discoveryPeriod: 2 * time.Minute, pollInterval: 5 * time.Second,
	}, nil
}

func sleep(ctx context.Context, delay time.Duration) error {
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

// Repository identifies one GitHub repository.
type Repository struct {
	Owner string
	Name  string
}

func (r Repository) validate() error {
	if strings.TrimSpace(r.Owner) == "" || strings.TrimSpace(r.Name) == "" ||
		strings.ContainsAny(r.Owner+r.Name, "/?#") {

		return fmt.Errorf("invalid GitHub repository %q/%q", r.Owner, r.Name)
	}
	return nil
}

func (r Repository) endpoint() string {
	return "repos/" + r.Owner + "/" + r.Name
}

// RepositoryMetadata contains repository fields commonly needed for allowlist checks.
type RepositoryMetadata struct {
	FullName      string `json:"full_name"`
	DefaultBranch string `json:"default_branch"`
}

// Workflow identifies one workflow and dispatch ref.
type Workflow struct {
	Repository       Repository
	File             string
	Ref              string
	CorrelationInput string
}

func (w Workflow) validate() error {
	if err := w.Repository.validate(); err != nil {
		return err
	}
	if strings.TrimSpace(w.File) == "" || strings.ContainsAny(w.File, "/?#") || strings.TrimSpace(w.Ref) == "" {
		return fmt.Errorf("invalid GitHub workflow %q at ref %q", w.File, w.Ref)
	}
	if strings.TrimSpace(w.CorrelationInput) != w.CorrelationInput || strings.ContainsAny(w.CorrelationInput, "/?#&= ") {
		return fmt.Errorf("invalid GitHub workflow correlation input %q", w.CorrelationInput)
	}
	return nil
}

func (w Workflow) endpoint() string {
	return w.Repository.endpoint() + "/actions/workflows/" + w.File
}

// WorkflowMetadata contains workflow fields needed for allowlist checks.
type WorkflowMetadata struct {
	Path  string `json:"path"`
	State string `json:"state"`
}

// PullRequest contains immutable pull request metadata.
type PullRequest struct {
	Number  int
	Title   string
	URL     string
	State   string
	Merged  bool
	BaseRef string
	HeadRef string
	HeadSHA string
	Fork    bool
	Labels  []string
}

// WorkflowRun is one GitHub Actions workflow run.
type WorkflowRun struct {
	ID           int64     `json:"id"`
	URL          string    `json:"html_url"`
	Status       string    `json:"status"`
	Conclusion   string    `json:"conclusion"`
	Event        string    `json:"event"`
	HeadBranch   string    `json:"head_branch"`
	HeadSHA      string    `json:"head_sha"`
	Path         string    `json:"path"`
	CreatedAt    time.Time `json:"created_at"`
	DisplayTitle string    `json:"display_title"`
	Actor        struct {
		Login string `json:"login"`
	} `json:"actor"`
}

// AuthStatus verifies local gh authentication for the configured hostname.
func (c *Client) AuthStatus(ctx context.Context) error {
	if _, err := c.runner.Run(ctx, nil, "auth", "status", "--hostname", c.host); err != nil {
		return fmt.Errorf("verify GitHub CLI authentication: %w", err)
	}
	return nil
}

// GetRepository reads repository metadata.
func (c *Client) GetRepository(ctx context.Context, repository Repository) (RepositoryMetadata, error) {
	if err := repository.validate(); err != nil {
		return RepositoryMetadata{}, err
	}
	var result RepositoryMetadata
	if err := c.getJSON(ctx, repository.endpoint(), &result); err != nil {
		return RepositoryMetadata{}, err
	}
	return result, nil
}

// GetWorkflow reads workflow metadata.
func (c *Client) GetWorkflow(ctx context.Context, workflow Workflow) (WorkflowMetadata, error) {
	if err := workflow.validate(); err != nil {
		return WorkflowMetadata{}, err
	}
	var result WorkflowMetadata
	if err := c.getJSON(ctx, workflow.endpoint(), &result); err != nil {
		return WorkflowMetadata{}, err
	}
	return result, nil
}

// GetFile reads and base64-decodes a repository file at ref.
func (c *Client) GetFile(ctx context.Context, repository Repository, path, ref string) ([]byte, error) {
	if err := repository.validate(); err != nil {
		return nil, err
	}
	if strings.TrimSpace(path) == "" || strings.ContainsAny(path, "?#") || strings.TrimSpace(ref) == "" {
		return nil, errors.New("invalid GitHub file path or ref")
	}
	var response struct {
		Encoding string `json:"encoding"`
		Content  string `json:"content"`
	}
	if err := c.getJSON(ctx, repository.endpoint()+"/contents/"+path+"?ref="+ref, &response); err != nil {
		return nil, err
	}
	if response.Encoding != "base64" {
		return nil, fmt.Errorf("GitHub file has unsupported content encoding %q", response.Encoding)
	}
	data, err := base64.StdEncoding.DecodeString(strings.ReplaceAll(response.Content, "\n", ""))
	if err != nil {
		return nil, fmt.Errorf("decode GitHub file: %w", err)
	}
	return data, nil
}

// GetPullRequest reads pull request metadata without applying repository-specific policy.
func (c *Client) GetPullRequest(ctx context.Context, repository Repository, number int) (PullRequest, error) {
	if err := repository.validate(); err != nil {
		return PullRequest{}, err
	}
	if number <= 0 {
		return PullRequest{}, errors.New("pull request number must be positive")
	}
	var response struct {
		Number int    `json:"number"`
		Title  string `json:"title"`
		State  string `json:"state"`
		Merged bool   `json:"merged"`
		Base   struct {
			Ref string `json:"ref"`
		} `json:"base"`
		Head struct {
			Ref  string `json:"ref"`
			SHA  string `json:"sha"`
			Repo *struct {
				Fork bool `json:"fork"`
			} `json:"repo"`
		} `json:"head"`
		Labels []struct {
			Name string `json:"name"`
		} `json:"labels"`
	}
	if err := c.getJSON(ctx, fmt.Sprintf("%s/pulls/%d", repository.endpoint(), number), &response); err != nil {
		return PullRequest{}, err
	}
	if response.Head.Repo == nil {
		return PullRequest{}, fmt.Errorf("pull request %d has no head repository", number)
	}
	result := PullRequest{
		Number: response.Number, Title: response.Title,
		URL:   fmt.Sprintf("https://%s/%s/%s/pull/%d", c.host, repository.Owner, repository.Name, number),
		State: response.State, Merged: response.Merged, BaseRef: response.Base.Ref,
		HeadRef: response.Head.Ref, HeadSHA: response.Head.SHA, Fork: response.Head.Repo.Fork,
	}
	for _, label := range response.Labels {
		result.Labels = append(result.Labels, label.Name)
	}
	return result, nil
}

// AddLabels adds labels to an issue or pull request.
func (c *Client) AddLabels(ctx context.Context, repository Repository, number int, labels []string) error {
	if err := repository.validate(); err != nil {
		return err
	}
	if number <= 0 || len(labels) == 0 {
		return errors.New("issue number and labels are required")
	}
	for _, label := range labels {
		if strings.TrimSpace(label) == "" {
			return errors.New("label is empty")
		}
	}
	body, err := json.Marshal(struct {
		Labels []string `json:"labels"`
	}{Labels: labels})
	if err != nil {
		return fmt.Errorf("encode labels request: %w", err)
	}
	_, err = c.runner.Run(
		ctx, body, "api", "--hostname", c.host, "--method", "POST",
		fmt.Sprintf("%s/issues/%d/labels", repository.endpoint(), number), "--input", "-",
	)
	return err
}

// DispatchWorkflow dispatches a workflow and discovers the resulting run for the authenticated actor.
func (c *Client) DispatchWorkflow(ctx context.Context, workflow Workflow, inputs map[string]string) (WorkflowRun, error) {
	if err := workflow.validate(); err != nil {
		return WorkflowRun{}, err
	}
	var user struct {
		Login string `json:"login"`
	}
	if err := c.getJSON(ctx, "user", &user); err != nil {
		return WorkflowRun{}, fmt.Errorf("read authenticated GitHub user: %w", err)
	}
	if user.Login == "" {
		return WorkflowRun{}, errors.New("authenticated GitHub user has an empty login")
	}
	before, err := c.listWorkflowRuns(ctx, workflow)
	if err != nil {
		return WorkflowRun{}, fmt.Errorf("list workflow runs before dispatch: %w", err)
	}
	known := make(map[int64]struct{}, len(before))
	for _, run := range before {
		known[run.ID] = struct{}{}
	}
	dispatchedAt := time.Now().UTC()
	dispatchInputs := make(map[string]string, len(inputs)+1)
	for name, value := range inputs {
		dispatchInputs[name] = value
	}
	correlationID := ""
	if workflow.CorrelationInput != "" {
		if _, exists := dispatchInputs[workflow.CorrelationInput]; exists {
			return WorkflowRun{}, fmt.Errorf("workflow input %q is reserved for dispatch correlation", workflow.CorrelationInput)
		}
		correlationID, err = c.newCorrelationID()
		if err != nil {
			return WorkflowRun{}, fmt.Errorf("generate workflow dispatch correlation ID: %w", err)
		}
		dispatchInputs[workflow.CorrelationInput] = correlationID
	}
	body, err := json.Marshal(struct {
		Ref    string            `json:"ref"`
		Inputs map[string]string `json:"inputs"`
	}{Ref: workflow.Ref, Inputs: dispatchInputs})
	if err != nil {
		return WorkflowRun{}, fmt.Errorf("encode workflow dispatch request: %w", err)
	}
	if _, err := c.runner.Run(ctx, body, "api", "--hostname", c.host, "--method", "POST", workflow.endpoint()+"/dispatches", "--input", "-"); err != nil {
		return WorkflowRun{}, fmt.Errorf("dispatch GitHub workflow: %w", err)
	}
	deadline := dispatchedAt.Add(c.discoveryPeriod)
	for {
		runs, err := c.listWorkflowRuns(ctx, workflow)
		if err == nil {
			var candidates []WorkflowRun
			for _, run := range runs {
				if _, exists := known[run.ID]; exists || run.CreatedAt.Before(dispatchedAt.Add(-5*time.Second)) || run.Actor.Login != user.Login {
					continue
				}
				if correlationID != "" && run.DisplayTitle != correlationID {
					continue
				}
				if err := c.ValidateWorkflowRun(workflow, run); err == nil {
					candidates = append(candidates, run)
				}
			}
			if len(candidates) == 1 {
				return candidates[0], nil
			}
			if len(candidates) > 1 {
				return WorkflowRun{}, errors.New("multiple new workflow runs appeared after dispatch; refusing ambiguous correlation")
			}
		}
		if time.Now().UTC().After(deadline) {
			return WorkflowRun{}, errors.New("timed out discovering the dispatched workflow run")
		}
		if err := c.sleep(ctx, c.pollInterval); err != nil {
			return WorkflowRun{}, err
		}
	}
}

func newCorrelationID() (string, error) {
	data := make([]byte, 16)
	if _, err := rand.Read(data); err != nil {
		return "", err
	}
	return "release-ui-" + hex.EncodeToString(data), nil
}

// PollWorkflowRun waits until the workflow run completes and reports each observed state.
func (c *Client) PollWorkflowRun(
	ctx context.Context,
	workflow Workflow,
	id int64,
	report func(WorkflowRun) error,
) (WorkflowRun, error) {
	if err := workflow.validate(); err != nil {
		return WorkflowRun{}, err
	}
	if id <= 0 {
		return WorkflowRun{}, fmt.Errorf("invalid workflow run ID %d", id)
	}
	for {
		var run WorkflowRun
		if err := c.getJSON(ctx, fmt.Sprintf("%s/actions/runs/%d", workflow.Repository.endpoint(), id), &run); err != nil {
			return WorkflowRun{}, fmt.Errorf("get workflow run %d: %w", id, err)
		}
		if run.ID != id {
			return WorkflowRun{}, fmt.Errorf("GitHub returned workflow run %d, expected %d", run.ID, id)
		}
		if err := c.ValidateWorkflowRun(workflow, run); err != nil {
			return WorkflowRun{}, err
		}
		if report != nil {
			if err := report(run); err != nil {
				return WorkflowRun{}, err
			}
		}
		switch run.Status {
		case "queued", "in_progress", "waiting", "pending", "requested":
			if err := c.sleep(ctx, c.pollInterval); err != nil {
				return WorkflowRun{}, err
			}
		case "completed":
			if run.Conclusion == "success" {
				return run, nil
			}
			return run, fmt.Errorf("workflow run %d completed with conclusion %q: %s", run.ID, run.Conclusion, run.URL)
		default:
			return run, fmt.Errorf("workflow run %d has unsupported status %q", run.ID, run.Status)
		}
	}
}

func (c *Client) listWorkflowRuns(ctx context.Context, workflow Workflow) ([]WorkflowRun, error) {
	var response struct {
		WorkflowRuns []WorkflowRun `json:"workflow_runs"`
	}
	endpoint := workflow.endpoint() + "/runs?event=workflow_dispatch&branch=" + workflow.Ref + "&per_page=20"
	if err := c.getJSON(ctx, endpoint, &response); err != nil {
		return nil, err
	}
	return response.WorkflowRuns, nil
}

// ValidateWorkflowRun verifies that a run belongs to workflow and has valid fixed metadata.
func (c *Client) ValidateWorkflowRun(workflow Workflow, run WorkflowRun) error {
	if err := workflow.validate(); err != nil {
		return err
	}
	if run.ID <= 0 || run.Event != "workflow_dispatch" || run.HeadBranch != workflow.Ref || run.Actor.Login == "" {
		return fmt.Errorf("workflow run does not match the dispatch target: %#v", run)
	}
	wantURL := fmt.Sprintf("https://%s/%s/%s/actions/runs/%d", c.host, workflow.Repository.Owner, workflow.Repository.Name, run.ID)
	wantPath := ".github/workflows/" + workflow.File
	if run.URL != wantURL || !strings.HasPrefix(run.Path, wantPath) {
		return fmt.Errorf("workflow run does not match the workflow target: %#v", run)
	}
	if len(run.HeadSHA) != 40 {
		return fmt.Errorf("workflow run %d has invalid head SHA %q", run.ID, run.HeadSHA)
	}
	if _, err := hex.DecodeString(run.HeadSHA); err != nil {
		return fmt.Errorf("workflow run %d has invalid head SHA %q", run.ID, run.HeadSHA)
	}
	if run.CreatedAt.IsZero() {
		return fmt.Errorf("workflow run %d has no creation time", run.ID)
	}
	return nil
}

func (c *Client) getJSON(ctx context.Context, endpoint string, target any) error {
	output, err := c.runner.Run(ctx, nil, "api", "--hostname", c.host, endpoint)
	if err != nil {
		return err
	}
	if err := json.Unmarshal(output, target); err != nil {
		return fmt.Errorf("decode GitHub response: %w", err)
	}
	return nil
}
