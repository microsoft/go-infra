// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package patch

import (
	"fmt"
	"log"
	"os/exec"
	"path/filepath"

	"github.com/microsoft/go-infra/gitcmd"
)

// MatchCheckRepo checks whether each patch in a series of patches makes actual changes, or if it is
// equivalent to an existing patch. The purpose is to let "git go-patch extract" ignore superficial
// differences like chunk line numbers and index hashes that would only distract code reviewers.
type MatchCheckRepo struct {
	gitDir                     string
	existingPatchPathsByHeader map[Header]string
}

// NewMatchCheckRepo clones the given submodule to a temp repo and prepares to search the patchesDir
// for a match to each patch passed to Apply. Returns the created context for this process. Call the
// context's AttemptDelete method to clean up the temp dir if desired.
func NewMatchCheckRepo(submodulePath, baseCommit, patchesDir string) (*MatchCheckRepo, error) {
	m := MatchCheckRepo{}
	var err error
	if m.gitDir, err = gitcmd.NewTempCloneRepo(submodulePath); err != nil {
		return nil, fmt.Errorf("failed to create temp clone of submodule to evaluate patch changes: %v", err)
	}
	if err := gitcmd.Run(m.gitDir, "checkout", "-q", baseCommit); err != nil {
		return nil, fmt.Errorf("failed to check out base commit in temp repo: %v", err)
	}
	// Associate each patch's header (identity, as far as this comparison is concerned) with the
	// patch file's path. This lets us try old and new when we find a new patch's header, later.
	m.existingPatchPathsByHeader = make(map[Header]string)
	if err := WalkPatches(patchesDir, func(path string) error {
		p, err := ReadFile(path)
		if err != nil {
			return err
		}
		absPath, err := filepath.Abs(path)
		if err != nil {
			return err
		}
		if alreadyFoundPath, ok := m.existingPatchPathsByHeader[p.Header]; ok {
			return fmt.Errorf(
				"found patches with identical headers in %#q: %#q and %#q",
				patchesDir, filepath.Base(alreadyFoundPath), filepath.Base(path))
		}
		m.existingPatchPathsByHeader[p.Header] = absPath
		return nil
	}); err != nil {
		return nil, fmt.Errorf("failed to read existing patch: %v", err)
	}
	return &m, nil
}

// CheckedApply applies the patch at path to the MatchCheckRepo while checking for matches. If a
// matching patch is found, that patch's path is returned.
//
// It is recommended to call this in a WalkPatches or WalkGoPatches fn: the method must be called
// once and only once for each patch, and the calls must be in the correct patch application order.
func (m *MatchCheckRepo) CheckedApply(path string, p *Patch) (string, error) {
	// Make sure path is absolute because we will pass it to Git running in a different working dir.
	absPath, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}

	// Create a commit using "am" for the existing/old patch we discover. If no matching patch is
	// found, empty string.
	var oldCommit string
	oldPatchPath, ok := m.existingPatchPathsByHeader[p.Header]
	if ok {
		// If we found a matching old patch, apply it, save the commit hash we created, and undo.
		base, err := gitcmd.RevParse(m.gitDir, "HEAD")
		if err != nil {
			return "", err
		}
		if err := gitcmd.Run(m.gitDir, "am", "-q", "--whitespace=nowarn", oldPatchPath); err != nil {
			// If the old patch no longer works at all, it definitely doesn't match the new patch!
			// Cancel the attempt.
			log.Printf("Old patch doesn't apply: %v. Disregarding this patch as a match candidate for %q and continuing...\n", err, filepath.Base(absPath))
			if err := gitcmd.Run(m.gitDir, "am", "--abort", "-q"); err != nil {
				return "", err
			}
		} else {
			// Old patch worked! Save the commit to compare against the new patch later.
			oldCommit, err = gitcmd.RevParse(m.gitDir, "HEAD")
			if err != nil {
				return "", err
			}
		}
		if err := gitcmd.Run(m.gitDir, "checkout", "-q", base); err != nil {
			return "", fmt.Errorf("failed to undo patch in tmp repo; %v: %v", `use "-verbatim" or fix the issue`, err)
		}
	}

	// Apply the given "new" patch. Don't undo this: we want future calls of Apply to build on this.
	if err := gitcmd.Run(m.gitDir, "am", "-q", "--whitespace=nowarn", absPath); err != nil {
		return "", err
	}

	if ok && oldCommit != "" {
		// Compare the result of applying the new patch vs. the old patch.
		if err := gitcmd.Run(m.gitDir, "diff", "--quiet", "HEAD", oldCommit); err != nil {
			if _, ok := err.(*exec.ExitError); !ok {
				return "", fmt.Errorf("unexpected error while comparing old and new patch; %v: %v", `use "-verbatim" or fix the issue`, err)
			}
			// Non-zero exit code means the different patches actually have different results in the
			// real repo. This means the existing patch is *not* a match.
			return "", nil
		}
		// Found an old patch with the same header as p, and even though the chunks and hashes in
		// the patch file might be different (e.g. based on different base commits), they have the
		// same result once applied to the repo. It's a match.
		return oldPatchPath, nil
	}
	// We didn't even find a matching header. No match.
	return "", nil
}

// AttemptDelete tries to delete the temp check repo dir. If an error occurs, log it, but this is
// not fatal. It is inside the user's temp dir, so it will be cleaned up later by the OS anyway.
func (m *MatchCheckRepo) AttemptDelete() {
	gitcmd.AttemptDelete(m.gitDir)
}
