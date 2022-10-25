// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package patch

import (
	"path/filepath"
)

// ConfigFileName is the name of the config file on disk. The name is similar to ".gitignore", etc.
const ConfigFileName = ".git-go-patch"

// conventionalConfig is the config used when no config file is detected and the fallback to
// microsoft/go detection finds a match.
var conventionalConfig = Config{
	SubmoduleDir:  "go",
	PatchesDir:    "patches",
	StatusFileDir: "eng/artifacts/go-patch",
}

// Config is the patch config file content. The default values are used as the default "init"
// content, so some fields are "omitempty" to avoid showing up by default.
type Config struct {
	// MinimumToolVersion, if defined, causes "git-go-patch" commands to refuse to run if the tool's
	// version is lower than this version. This can be used to introduce features into the
	// "git-go-patch" tool that don't work in previous versions, like new patch commands.
	MinimumToolVersion string

	// SubmoduleDir is the submodule directory to patch, relative to the config file.
	SubmoduleDir string
	// PatchesDir is the directory with patch files to use, relative to the config file.
	PatchesDir string
	// StatusFileDir is a gitignored directory to put workflow-related temporary status files,
	// relative to the config file.
	StatusFileDir string

	// ExtractAsAuthor makes "git-go-patch extract" set this author in the resulting patch file.
	//
	// This can be used to avoid patch file attribution confusion: when a patch goes through the
	// "apply" "rebase" "extract" process, the patch file's "author" field stays the same, but
	// someone else changed the content. This can make it appear that an author understands (or
	// endorses) a change, although they might not. Using this setting with (e.g.) a bot account
	// allows the project to consistently assign a single author to all patch files to avoid the
	// confusion. The correct way to see the contributors to a patch file is to view the patch
	// file's history, not the author line of the patch file.
	//
	// Example: "microsoft-golang-bot <microsoft-golang-bot@users.noreply.github.com>"
	ExtractAsAuthor string `json:",omitempty"`
}

// FoundConfig is a Config file's contents plus the location the config file was found, the RootDir.
type FoundConfig struct {
	Config
	RootDir string
}

// FullProjectRoots returns the full path for the project and submodule root dirs. This function is
// provided for convenience while migrating old code onto the config file API.
func (c *FoundConfig) FullProjectRoots() (rootDir, submoduleDir string) {
	return c.RootDir, filepath.Join(c.RootDir, c.SubmoduleDir)
}

// FullStatusFileDir is the full status file dir path.
func (c *FoundConfig) FullStatusFileDir() string {
	return filepath.Join(c.RootDir, c.StatusFileDir)
}

// FullPrePatchStatusFilePath is the full path to the "pre-patch" status file, which may store the
// commit hash before a "git go-patch apply" so "git go-patch extract" can use it later.
func (c *FoundConfig) FullPrePatchStatusFilePath() string {
	return filepath.Join(c.FullStatusFileDir(), "HEAD_BEFORE_APPLY")
}

// FullPostPatchStatusFilePath is the full path to the "post-patch" status file. This may be used to
// determine if the submodule is fresh (the dev just ran "git go-patch apply" and nothing else) or
// if it might contain dev work that shouldn't be discarded.
func (c *FoundConfig) FullPostPatchStatusFilePath() string {
	return filepath.Join(c.FullStatusFileDir(), "HEAD_AFTER_APPLY")
}
