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

	// SourceBranches is the list of branches in Upstream to merge into Target.
	SourceBranches []string

	// AutoResolveOurs contains files and dirs that upstream may modify, but we want to ignore those
	// modifications and keep our changes to them. Normally our files are all in the 'eng/'
	// directory, but some files are required by GitHub to be in the root of the repo or in the
	// '.github' directory. In those cases, we must modify them in place and auto-resolve conflicts.
	AutoResolveOurs []string
}
