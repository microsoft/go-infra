// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

// Package buildreport helps maintain tracking issues containing a list of build status entries in a
// table that may be updated concurrently by multiple data sources. This is used in the release
// process to keep track of builds for multiple releases happening at the same time.
package buildreport

import (
	"context"
	_ "embed"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/google/go-github/v65/github"
	"github.com/microsoft/go-infra/gitcmd"
	"github.com/microsoft/go-infra/githubutil"
	"github.com/microsoft/go-infra/goversion"
	"github.com/microsoft/go-infra/stringutil"
)

// Symbols are used to indicate report entry status.
const (
	SymbolFailed     = "❌"
	SymbolSucceeded  = "✅"
	SymbolInProgress = "🏃"
	SymbolNotStarted = "⌚"
)

const symbolKey = "" + SymbolNotStarted + " Waiting for first report, " +
	SymbolInProgress + " In progress, " +
	SymbolFailed + " Failed, " +
	SymbolSucceeded + " Succeeded"

const (
	dataSectionMarker      = "section generated by go-infra './cmd/releasego report'."
	beginDataSectionMarker = "<!-- BEGIN " + dataSectionMarker + " -->"
	endDataSectionMarker   = "<!-- END " + dataSectionMarker + " -->"
)

const (
	beginDataMarker = "<!-- DATA "
	endDataMarker   = " DATA -->"
)

const (
	reportRetryTimeoutDuration = time.Minute
	reportRetryDelayDuration   = 3 * time.Second
)

const (
	githubWikiDefaultBranch = "refs/heads/master"
	localTempBranch         = "refs/heads/buildreport-temp"
)

const (
	releaseBuildPipelineName  = "microsoft-go-infra-release-build"
	releaseImagesPipelineName = "microsoft-go-infra-release-go-images"
)

var pipelinesWithRetryInstructions = map[string]struct{}{
	releaseBuildPipelineName:  {},
	releaseImagesPipelineName: {},
}

// Update updates the report then sends a notification comment if necessary.
func Update(ctx context.Context, owner, repoName, pat string, issue int, s State) error {
	if err := UpdateIssueBody(ctx, owner, repoName, pat, issue, s); err != nil {
		return err
	}
	return Notify(ctx, owner, repoName, pat, issue, s)
}

// UpdateIssueBody updates the given issue with new state. Requires the target GitHub repo to have
// the wiki activated to perform safer concurrent updates than a simple issue description edit.
func UpdateIssueBody(ctx context.Context, owner, repoName, pat string, issue int, s State) error {
	client, err := githubutil.NewClient(ctx, pat)
	if err != nil {
		return err
	}

	// To handle concurrent edits, use Git and "git push".
	//
	// It would be preferable to stick with the GitHub API to avoid local data, but editing issue
	// descriptions isn't safe concurrently. The ETags provided are weak, so If-Match doesn't work.
	// If-Unmodified-Since seems to be ignored. The "update ref without force update" API allows
	// forced updates if the API calls happen close together.
	auther := githubutil.GitHubPATAuther{
		PAT: pat,
	}
	// Use the wiki to store the data. This makes it visible without causing noise in the main repo.
	url := "https://github.com/" + owner + "/" + repoName + ".wiki.git"

	gitDir, err := gitcmd.NewTempGitRepo()
	if err != nil {
		return err
	}
	defer gitcmd.AttemptDelete(gitDir)

	pageName := fmt.Sprintf("releasego-report-for-issue-%v", issue)
	dataFilename := fmt.Sprintf("%v.md", pageName)
	dataPath := filepath.Join(gitDir, dataFilename)

	var body string

	startTime := time.Now()
	for {
		elapsed := time.Since(startTime)
		if elapsed > reportRetryTimeoutDuration {
			return fmt.Errorf("retry timeout %v expended", reportRetryTimeoutDuration)
		}

		// githubutil.Retry is designed to handle infra flakiness and rate limiting. We want this,
		// but we also want to handle potential concurrency issues. So: use two layers of retry.
		err := githubutil.Retry(func() error {
			if err := gitcmd.Run(gitDir, "fetch", "--depth", "1", auther.InsertAuth(url), githubWikiDefaultBranch+":"+localTempBranch, "-f"); err != nil {
				return err
			}
			if err := gitcmd.Run(gitDir, "checkout", "-f", "--detach", localTempBranch); err != nil {
				return err
			}

			var existingBody string
			if existingBodyBytes, err := os.ReadFile(dataPath); err != nil {
				log.Printf("Failed to read %q (%v), fetching issue content for initial commit", dataFilename, err)
				githubIssue, _, err := client.Issues.Get(ctx, owner, repoName, issue)
				if err != nil {
					return err
				}
				existingBody = githubIssue.GetBody()
			} else {
				existingBody = string(existingBodyBytes)
			}

			rc := parseReportComment(existingBody)
			rc.update(s)

			// Tweak body generation fields that only apply to the issue body, not notifications.
			rc.wikiURL = "https://github.com/" + owner + "/" + repoName + "/wiki/" + pageName
			rc.key = true

			body, err = rc.body()
			if err != nil {
				return fmt.Errorf("unable to generate issue body: %v", err)
			}

			if err := os.WriteFile(dataPath, []byte(body), 0o666); err != nil {
				return err
			}
			if err := gitcmd.Run(gitDir, "add", "--", dataFilename); err != nil {
				return err
			}
			if err := gitcmd.Run(gitDir, "commit", "-m", "Update "+dataFilename); err != nil {
				return err
			}
			return gitcmd.Run(gitDir, "push", auther.InsertAuth(url), "HEAD:"+githubWikiDefaultBranch)
		})
		if err != nil {
			// Inner retry wasn't able to get the update done. This may be due to concurrency: N
			// builds trying to update the issue at the same time. Try again after a short delay.
			// (Nothing fancy: even in the worst case of N updates happening simultaneously,
			// repeatedly, all concurrent updates will eventually be able to get through because one
			// update out of N succeeds each time.)
			log.Printf(
				"Inner GitHub retry loop failed. Waiting %v then trying again. Will give up after %v. Error: %v\n",
				reportRetryDelayDuration,
				reportRetryDelayDuration-elapsed,
				err)
			time.Sleep(reportRetryDelayDuration)
			continue
		}
		break // Success.
	}
	// Now that we've successfully pushed, the data is saved, and we know it's the latest
	// available data. Update the issue.
	//
	// There is potential for a conflict with another simultaneous UpdateIssueBody call here: if
	// there is a delay between "push" and Edit, and another UpdateIssueBody call starts and
	// finishes during that delay, this Edit will revert the issue to show old data!
	//
	// Mitigations: there isn't any known reason we'd have a long delay right here. The data
	// itself is safe: the next UpdateIssueBody call will fix the issue and post the correct
	// data. The report includes a link to the actual data in case it's important to get the
	// real data during a release.
	log.Printf("Copying report to https://github.com/%v/%v/issues/%v description...", owner, repoName, issue)
	return githubutil.Retry(func() error {
		edit, _, err := client.Issues.Edit(ctx, owner, repoName, issue, &github.IssueRequest{Body: &body})
		if err != nil {
			return err
		}
		log.Printf("Edit successful:\n%v\n", edit.ID)
		return nil
	})
}

// Notify determines if a notification is necessary for the given status update and sends it.
func Notify(ctx context.Context, owner string, repoName string, pat string, issue int, s State) error {
	notification := s.notificationPreamble()
	if notification == "" {
		return nil
	}

	client, err := githubutil.NewClient(ctx, pat)
	if err != nil {
		return err
	}

	return githubutil.Retry(func() error {
		c := commentBody{reports: []State{s}}
		body, err := c.body()
		if err != nil {
			return err
		}

		notification += body +
			"\n<sub>See the [issue description](" +
			"https://github.com/" + owner + "/" + repoName + "/issues/" + strconv.Itoa(issue) +
			") for the latest build status.</sub>"

		notificationComment, _, err := client.Issues.CreateComment(
			ctx, owner, repoName, issue, &github.IssueComment{Body: &notification})
		if err != nil {
			return err
		}
		log.Printf("Notification comment: %v\n", *notificationComment.HTMLURL)
		return nil
	})
}

// State is the status of one entry in the report.
type State struct {
	// ID of the report. If an AzDO build, the AzDO Build ID.
	ID string
	// Version this report is associated with.
	Version string
	// Name of this type of report. If an AzDO build, the pipeline name.
	Name string
	// URL that the ID should link to. If an AzDO build, the main build page.
	URL string
	// Status represents the status.
	Status string

	LastUpdate time.Time
	StartTime  time.Time
}

func (s *State) updateFrom(source State) {
	if source.Version != "" {
		s.Version = source.Version
	}
	if source.Name != "" {
		s.Name = source.Name
	}
	if source.URL != "" {
		s.URL = source.URL
	}
	if source.Status != "" {
		s.Status = source.Status
	}
	if !source.LastUpdate.IsZero() {
		s.LastUpdate = source.LastUpdate
	}
	if !source.StartTime.IsZero() {
		s.StartTime = source.StartTime
	}
}

func (s *State) notificationPreamble() string {
	switch s.Status {
	case SymbolFailed:
		return "Build failed!\n"
	case SymbolSucceeded:
		switch s.Name {
		case releaseBuildPipelineName:
			notification := "Completed releasing one version! If every version's release-build and innerloop tests are successful, approve the release-go-images build to continue.\n"
			if goversion.New(s.Version).Patch == "0" {
				notification += "\nThis appears to be a new major version! If so, here are a few things that will need an update when the release is done:\n" +
					"* [ ] The [microsoft/go download links](https://github.com/microsoft/go/blob/microsoft/main/eng/doc/Downloads.md)\n" +
					"* [ ] The [internal site's download links](https://eng.ms/docs/more/languages-at-microsoft/go/articles/overview#go-releases)\n" +
					"* [ ] The [recommended Docker tags](https://github.com/microsoft/go-images#recommended-tags)\n"
			}
			return notification
		case releaseImagesPipelineName:
			return "Completed building all images!\n\n" +
				"Next, [announce the release](https://github.com/microsoft/go-infra/blob/main/docs/release-process/instructions.md#making-the-internal-announcement).\n"
		}
	}
	return ""
}

type commentBody struct {
	before, after string
	reports       []State
	// wikiURL links to the data source for the comment body, if any. This is a pointer to a GitHub
	// wiki page used to synchronize updates.
	wikiURL string
	// key indicates the generated body should include a key for the status symbols.
	key bool
}

func parseReportComment(body string) commentBody {
	before, report, after, found := stringutil.CutTwice(body, beginDataSectionMarker, endDataSectionMarker)
	if found {
		if _, data, _, found := stringutil.CutTwice(report, beginDataMarker, endDataMarker); found {
			r := make([]State, 0) // Unmarshal doesn't accept nil.
			err := json.Unmarshal([]byte(data), &r)
			if err != nil {
				log.Printf("Unable to read report data from comment: %v\n%v\n", err, data)
			}
			return commentBody{
				before: before, after: after,
				reports: r,
			}
		}
	}
	// Either the BEGIN/END markers couldn't be found, or DATA couldn't be found. Keep before and
	// after (if found), but ignore the content of the data section (if any).
	return commentBody{
		before: before,
		after:  after,
	}
}

func (c *commentBody) update(report State) {
	found := false
	for i := range c.reports {
		r := &c.reports[i]
		if r.ID == report.ID {
			// Update the found report.
			r.updateFrom(report)
			found = true
			break
		}
	}
	if !found {
		c.reports = append(c.reports, report)
	}
}

func (c *commentBody) body() (string, error) {
	var b strings.Builder
	b.WriteString(c.before)
	// We can properly parse a comment when its text runs directly into the data markers, but for
	// ease of reading the generated Markdown source manually, insert newlines. Make sure there are
	// two, so it looks the same as a paragraph break.
	if c.before != "" && !strings.HasSuffix(c.before, "\n") {
		b.WriteString("\n")
		if !strings.HasSuffix(c.before, "\n\n") {
			b.WriteString("\n")
		}
	}
	b.WriteString(beginDataSectionMarker)
	b.WriteString("\n")

	sort.SliceStable(c.reports, func(i, j int) bool {
		iv, jv := c.reports[i], c.reports[j]
		if c := strings.Compare(iv.Version, jv.Version); c != 0 {
			return c < 0
		}
		if c := strings.Compare(iv.Name, jv.Name); c != 0 {
			return c < 0
		}
		if c := strings.Compare(iv.ID, jv.ID); c != 0 {
			return c < 0
		}
		return false
	})

	var version, name string
	// Always start a new table for the first report, even if it has no version or no name.
	newTable := true
	for _, r := range c.reports {
		if r.Version != version {
			version = r.Version
			b.WriteString("\n## ")
			b.WriteString(version)
			b.WriteString("\n")
			name = ""
			newTable = true
		}
		if r.Name != name {
			name = r.Name
			b.WriteString("\n### ")
			b.WriteString(name)
			b.WriteString("\n\n")
			newTable = true
		}
		if newTable {
			newTable = false
			b.WriteString("| ID | Status | Started | Last Report |\n")
			b.WriteString("| --- | :---: | --- | --- |\n")
		}
		b.WriteString("| ")
		if r.URL != "" {
			b.WriteString("[")
		}
		b.WriteString(r.ID)
		if r.URL != "" {
			b.WriteString("](")
			b.WriteString(r.URL)
			b.WriteString(")")
			// If the build has failed (potentially needs retry) and is a release infra build that
			// publishes detailed retry information on the "Extensions" tab, then show a direct
			// link.
			if r.Status == SymbolFailed {
				if _, ok := pipelinesWithRetryInstructions[r.Name]; ok {
					b.WriteString(" ([Retry](")
					b.WriteString(r.URL)
					b.WriteString("&view=ms.vss-build-web.run-extensions-tab))")
				}
			}
		}
		b.WriteString(" | ")
		b.WriteString(r.Status)
		b.WriteString(" | ")
		if !r.StartTime.IsZero() {
			b.WriteString(r.StartTime.Format("2006-01-02 15:04 MST"))
		}
		b.WriteString(" | ")
		if !r.LastUpdate.IsZero() {
			b.WriteString(r.LastUpdate.Format("2006-01-02 15:04 MST"))
		}
		b.WriteString(" |")
		b.WriteString("\n")
	}
	b.WriteString("\n")
	if c.key && len(c.reports) > 0 {
		b.WriteString(symbolKey)
		b.WriteString("  \n")
	}

	if c.wikiURL != "" {
		b.WriteString("<sub>This data is maintained in a [GitHub wiki page](")
		b.WriteString(c.wikiURL)
		b.WriteString("). The text above is a complete copy. Edits to the GitHub issue will be discarded.</sub>\n\n")
	}

	b.WriteString(beginDataMarker)
	bytes, err := json.MarshalIndent(c.reports, "", "  ")
	if err != nil {
		b.WriteString("[]")
	} else {
		b.Write(bytes)
	}
	b.WriteString(endDataMarker)
	b.WriteString("\n")

	b.WriteString(endDataSectionMarker)
	// Just like we made sure the begin marker was preceded by newlines, make sure the end marker is
	// succeeded by two newlines.
	if c.after != "" && !strings.HasPrefix(c.after, "\n") {
		b.WriteString("\n")
		if !strings.HasPrefix(c.after, "\n\n") {
			b.WriteString("\n")
		}
	}
	b.WriteString(c.after)
	return b.String(), nil
}
