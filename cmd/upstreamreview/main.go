// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

// Command upstreamreview serves the upstream-crypto-review GitHub App webhook.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"time"

	"github.com/google/go-github/v92/github"
	"github.com/microsoft/go-infra/githubutil"
)

func main() {
	addr := flag.String("listen", "127.0.0.1:8080", "HTTP address behind a TLS reverse proxy")
	mirror := flag.String("mirror", "", "operator-managed Git mirror of golang/go")
	repo := flag.String("repo", "microsoft/go", "installation repository (owner/name)")
	team := flag.String("team", "golang-compiler", "reviewing team slug in the repository's organization")
	flag.Parse()
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	owner, name, ok := strings.Cut(*repo, "/")
	if !ok || owner == "" || name == "" || strings.Contains(name, "/") || *team == "" || *mirror == "" {
		log.Fatal("a mirror, owner/repo, and reviewing team are required")
	}
	installation, err := strconv.ParseInt(os.Getenv("GITHUB_APP_INSTALLATION_ID"), 10, 64)
	if err != nil || installation <= 0 {
		log.Fatal("set GITHUB_APP_INSTALLATION_ID to a positive installation ID")
	}
	secret := os.Getenv("GITHUB_WEBHOOK_SECRET")
	if secret == "" {
		log.Fatal("set GITHUB_WEBHOOK_SECRET")
	}
	auth := githubutil.GitHubAuthFlags{
		GitHubPat: new(string), GitHubAppClientID: new(os.Getenv("GITHUB_APP_CLIENT_ID")),
		GitHubAppInstallation: &installation, GitHubAppPrivateKey: new(os.Getenv("GITHUB_APP_PRIVATE_KEY")),
	}
	client, err := auth.NewClient(ctx)
	if err != nil {
		log.Fatal(err)
	}
	appClient, err := auth.NewAppClient(ctx)
	if err != nil {
		log.Fatal(err)
	}
	app, _, err := appClient.Apps.Get(ctx, "")
	if err != nil {
		log.Fatal(err)
	}
	g := &gate{client: client, owner: owner, repo: name, team: *team, slug: app.GetSlug(), compare: compareMirror(*mirror), scans: make(map[int]result)}
	queue := make(chan work, 256)
	go g.worker(ctx, queue)
	server := &http.Server{
		Addr: *addr, Handler: webhook(*repo, installation, []byte(secret), queue),
		ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 10 * time.Second, WriteTimeout: 10 * time.Second,
	}
	go func() {
		<-ctx.Done()
		if err := server.Close(); err != nil {
			log.Print(err)
		}
	}()
	if err := server.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		log.Fatal(err)
	}
}

type work struct {
	number int
	base   string // Push: reconcile open PRs targeting this branch.
}

func webhook(repo string, installation int64, secret []byte, queue chan<- work) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/webhook" {
			http.NotFound(w, r)
			return
		}
		sig := r.Header.Get("X-Hub-Signature-256")
		if !strings.HasPrefix(sig, "sha256=") {
			http.Error(w, "missing signature", http.StatusUnauthorized)
			return
		}
		body, err := github.ValidatePayloadFromBody(r.Header.Get("Content-Type"), http.MaxBytesReader(w, r.Body, 2<<20), sig, secret)
		if err != nil {
			http.Error(w, "invalid signed payload", http.StatusUnauthorized)
			return
		}
		// Webhooks are notifications, not a source of approval or revision data.
		var event struct {
			Action       string
			Ref          string
			Repository   github.Repository
			Installation github.Installation
			PullRequest  github.PullRequest `json:"pull_request"`
		}
		if err := json.Unmarshal(body, &event); err != nil {
			http.Error(w, "invalid JSON", http.StatusBadRequest)
			return
		}
		if event.Installation.GetID() != installation || !strings.EqualFold(event.Repository.GetFullName(), repo) {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		var job work
		switch github.WebHookType(r) {
		case "pull_request":
			switch event.Action {
			case "opened", "reopened", "synchronize", "edited", "ready_for_review", "closed":
			default:
				w.WriteHeader(http.StatusNoContent)
				return
			}
			job.number = event.PullRequest.GetNumber()
		case "pull_request_review":
			job.number = event.PullRequest.GetNumber()
		case "push":
			job.base, _ = strings.CutPrefix(event.Ref, "refs/heads/")
			if job.base == event.Ref {
				w.WriteHeader(http.StatusNoContent)
				return
			}
		default:
			w.WriteHeader(http.StatusNoContent)
			return
		}
		if job.number <= 0 && job.base == "" {
			http.Error(w, "missing PR or branch", http.StatusBadRequest)
			return
		}
		select {
		case queue <- job:
			w.WriteHeader(http.StatusAccepted)
		default:
			http.Error(w, "busy", http.StatusServiceUnavailable)
		}
	})
}

// Serialize reconciles so redelivered/out-of-order notifications can't publish
// an older decision after a newer one. A sweep recovers missed events and retries
// transient failures (including GitHub's asynchronous test merge generation).
func (g *gate) worker(ctx context.Context, queue <-chan work) {
	tick := time.NewTicker(5 * time.Minute)
	defer tick.Stop()
	g.sweep(ctx, "")
	for {
		select {
		case <-ctx.Done():
			return
		case job := <-queue:
			if job.number > 0 {
				g.runPR(ctx, job.number)
			} else {
				g.sweep(ctx, job.base)
			}
		case <-tick.C:
			g.sweep(ctx, "")
		}
	}
}

func (g *gate) runPR(ctx context.Context, n int) {
	ctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	if err := g.reconcile(ctx, n); err != nil {
		log.Printf("PR #%d: %v", n, err)
	}
}

func (g *gate) sweep(ctx context.Context, base string) {
	opts := &github.PullRequestListOptions{Base: base, State: "open", ListOptions: github.ListOptions{PerPage: 100}}
	open := make(map[int]bool)
	for {
		prs, resp, err := g.client.PullRequests.List(ctx, g.owner, g.repo, opts)
		if err != nil {
			log.Print(fmt.Errorf("list open PRs: %w", err))
			return
		}
		for _, pr := range prs {
			open[pr.GetNumber()] = true
			g.runPR(ctx, pr.GetNumber())
		}
		if resp.NextPage == 0 {
			break
		}
		opts.Page = resp.NextPage
	}
	if base == "" {
		for n := range g.scans {
			if !open[n] {
				delete(g.scans, n)
			}
		}
	}
}
