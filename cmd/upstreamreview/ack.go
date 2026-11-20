// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package main

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/google/go-github/v92/github"
)

// The label ID identifies the label definition, not a particular application
// of it. Use the issue event ID so removing and re-adding a label revokes the
// previous acknowledgment, even if a workflow missed the removal.
type labelApplication struct {
	id, actor int64
	at        time.Time
	ambiguous bool
}

func (g *gate) currentLabelApplication(ctx context.Context, n int) (labelApplication, error) {
	var active labelApplication
	var lastAdded time.Time
	opts := &github.ListOptions{PerPage: 100}
	for {
		events, resp, err := g.client.Issues.ListIssueEvents(ctx, g.owner, g.repo, n, opts)
		if err != nil {
			return labelApplication{}, fmt.Errorf("read label history: %w", err)
		}
		for _, e := range events {
			if e.GetLabel().GetName() != g.policy.label {
				continue
			}
			switch e.GetEvent() {
			case "labeled":
				at := e.GetCreatedAt().Time
				active = labelApplication{id: e.GetID(), actor: e.GetActor().GetID(), at: at, ambiguous: at.Equal(lastAdded)}
				lastAdded = at
			case "unlabeled":
				active = labelApplication{}
			}
		}
		if resp.NextPage == 0 {
			return active, nil
		}
		opts.Page = resp.NextPage
	}
}

func (a labelApplication) matchesEvent(e *github.PullRequestEvent, repo string, n int, label string, scan scanResult) bool {
	pr := e.GetPullRequest()
	// A webhook includes the revisions at labeling, but not the issue event
	// ID. Don't let a replay stand for a later application of the same label.
	return e.GetAction() == "labeled" && e.GetLabel().GetName() == label &&
		e.GetRepo().GetFullName() == repo && e.GetNumber() == n &&
		pr.GetBase().GetSHA() == scan.Base && pr.GetHead().GetSHA() == scan.Head &&
		a.actor != 0 && e.GetSender().GetID() == a.actor && !a.ambiguous &&
		a.existedAt(e)
}

func (a labelApplication) existedAt(e *github.PullRequestEvent) bool {
	return !a.at.IsZero() && !a.at.After(e.GetPullRequest().GetUpdatedAt().Time)
}

// Only an acknowledged verdict writes this line to the bot's summary comment.
func (s scanResult) ackMarker() string {
	return fmt.Sprintf("\n<!-- upstreamreview:ack %d %s %s -->", s.LabelEvent, s.Old, s.New)
}

func (s scanResult) recordedIn(c *github.IssueComment) bool {
	return s.LabelEvent != 0 && strings.HasSuffix(strings.TrimSpace(c.GetBody()), s.ackMarker())
}
