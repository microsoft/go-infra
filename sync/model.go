// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package sync

import (
	"fmt"
	"path/filepath"
	"strings"
)

// ConfigEntry is one entry in a sync config file. The file contains a JSON list of objects that
// match this struct.
type ConfigEntry struct {
	// Upstream is the upstream Git repository to take updates from.
	Upstream string
	// UpstreamMirror is an optional upstream-maintained mirror of the Upstream repository.
	// Specifically, for Upstream 'https://go.googlesource.com/go', the UpstreamMirror is
	// 'https://github.com/golang/go'.
	//
	// When updating a submodule, sync checks both repos and only updates to a version that is in
	// both. This ensures our dev process doesn't have a strong dependency on one copy of upstream's
	// code or the other, and makes it reasonable to point the submodule at the GitHub mirror of Go
	// by default, which has a better appearance in the GitHub UI for a submodule: a clickable link.
	//
	// When merging a fork, the check is skipped. A fork inherently mirrors the upstream code, so
	// there is no reason to look for a common version and hold back.
	//
	// If UpstreamMirror is not defined (default), the update simply uses the latest version of
	// Upstream.
	UpstreamMirror string

	// Target is the GitHub repository to merge into, then submit the PR onto. It must be an https
	// github.com URL. Other arguments passed to the sync tool may transform this URL into a
	// different URL that works with authentication.
	Target string
	// Head is the GitHub repository to store the merged branch on. If not specified, defaults to
	// the value of Target. This can be used to run the PR from a GitHub fork.
	Head string
	// MirrorTarget	is an optional Git repository to push the upstream branch to. All mirroring
	// operations must succeed before sync continues with this sync config entry. The mirror target
	// is intended to be an internal repo, for reliability and security purposes.
	MirrorTarget string

	// BranchMap is a map of source branch names in Upstream (keys) to use to update a corresponding
	// branch in Target (values), where Target is either a fork repo of Upstream or contains a
	// submodule of Upstream. The key is glob matched. If the value contains "?" (not a valid branch
	// character), "?" is replaced with the upstream branch name.
	BranchMap map[string]string

	// AutoSyncBranches is the list of branches that a call to "./cmd/sync" should bring up to date.
	// It should be a list of keys that match up with BranchMap entries. The "./cmd/releasego sync"
	// command ignores this list, syncing a user-specified branch instead that may not even be in
	// the AutoSyncBranches list.
	AutoSyncBranches []string

	// MainBranch is the main/master branch of the target repository. When creating a new release
	// branch, it is forked from the tip of this branch.
	MainBranch string

	// SourceBranchLatestCommit is a map of source branch names in Upstream (keys) and a full commit
	// hash to treat as the latest commit for that source branch, no matter what the upstream
	// repository says at the time of merge. This map can be used to avoid a race between the sync
	// infrastructure and upstream merge flow.
	SourceBranchLatestCommit map[string]string

	// AutoResolveTarget lists files and dirs that Upstream may have modified, but we want to keep
	// the contents as they are in Target. Normally files that are modified in our fork repos are
	// all in the 'eng/' directory to avoid merge conflicts (and keep the repository tidy), but in
	// some cases this isn't possible. In these cases, Target has in-place modifications that must
	// be auto-resolved during the sync process.
	AutoResolveTarget []string
	// SubmoduleTarget is the path of a submodule in the Target repo to update with the latest
	// version of Upstream and UpstreamMirror (if specified). If this option is not specified
	// (default), that indicates the entire Upstream repository should be merged into the Target
	// repository.
	SubmoduleTarget string

	// GoVersionFileContent	is empty, or the Go version that the microsoft/go build should use
	// after the sync. Should be in the upstream format, e.g. go1.17.10 and go1.18. Sync examines
	// VERSION in the submodule, and if it doesn't match the expected value, creates/updates VERSION
	// in the outer repo (microsoft/go) to specify it. Otherwise, cleans up the outer VERSION file.
	GoVersionFileContent string

	// GoMicrosoftRevisionFileContent is empty, or the Microsoft revision (1, 2, ...) that the
	// microsoft/go build should use after the sync. If 1, removes the MICROSOFT_REVISION file if
	// one exists. If 2 or more, creates a MICROSOFT_REVISION file to specify it.
	GoMicrosoftRevisionFileContent string
}

// PRBranchStorageRepo returns the repo to store the PR branch on.
func (c *ConfigEntry) PRBranchStorageRepo() string {
	if c.Head != "" {
		return c.Head
	}
	return c.Target
}

// TargetBranch takes an upstream branch and returns the corresponding target branch. Returns an
// error if multiple target branches are found. Returns an empty string if no matches found.
func (c *ConfigEntry) TargetBranch(upstream string) (string, error) {
	matchedPatterns := make([]string, 0, 1)
	var foundTarget string
	for pattern, target := range c.BranchMap {
		found, err := filepath.Match(pattern, upstream)
		if err != nil {
			return "", err
		}
		if found {
			matchedPatterns = append(matchedPatterns, pattern)
			foundTarget = strings.ReplaceAll(target, "?", upstream)
		}
	}
	if len(matchedPatterns) > 1 {
		return "", fmt.Errorf("found more than one target branch match: %v", matchedPatterns)
	}
	return foundTarget, nil
}
