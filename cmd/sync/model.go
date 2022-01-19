// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package main

// SyncConfigEntry is one entry in a sync config file. The file contains a JSON list of objects that
// match this struct.
type SyncConfigEntry struct {
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
	// submodule of Upstream. If the value contains "?" (not a valid branch character), "?" is
	// replaced with the upstream branch name.
	BranchMap map[string]string

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
}
