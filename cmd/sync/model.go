// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package main

// SyncConfigEntry is one entry in a sync config file. The file contains a JSON list of objects that
// match this struct.
type SyncConfigEntry struct {
	// Upstream is the upstream Git repository to take updates from.
	Upstream string
	// Target is the GitHub repository to merge into, then submit the PR onto. It must be an https
	// github.com URL. Other arguments passed to the sync tool may transform this URL into a
	// different URL that works with authentication.
	Target string
	// Head is the GitHub repository to store the merged branch on. If not specified, defaults to
	// the value of Target. This can be used to run the PR from a GitHub fork.
	Head string

	// UpstreamMergeBranches is the list of branches in Upstream to merge into Target, where Target
	// is a fork repo of Upstream. The branch name in Target associated with each Upstream branch is
	// determined automatically, including a "microsoft/" prefix to distinguish it.
	UpstreamMergeBranches []string
	// MergeMap is a map of source branch name in Upstream to target branch name in Target. This map
	// should only be used by the configuration file when there is no reasonable way to determine
	// the target branch name automatically, like UpstreamMergeBranches.
	MergeMap map[string]string

	// AutoResolveTarget lists files and dirs that Upstream may have modified, but we want to keep
	// the contents as they are in Target. Normally files that are modified in our fork repos are
	// all in the 'eng/' directory to avoid merge conflicts (and keep the repository tidy), but in
	// some cases this isn't possible. In these cases, Target has in-place modifications that must
	// be auto-resolved during the sync process.
	AutoResolveTarget []string
}
