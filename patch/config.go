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

// Config is the patch config file content.
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
