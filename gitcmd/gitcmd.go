// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

// Package gitcmd contains utilities for common Git operations in a local repository, including
// authentication with a remote repository.
package gitcmd

import (
	"archive/tar"
	"bytes"
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/microsoft/go-infra/executil"
)

// Poll repeatedly checks using the given checker until it returns a successful result.
func Poll(checker PollChecker, delay time.Duration) string {
	for {
		result, err := checker.Check()
		if err == nil {
			log.Printf("Check suceeded, result: %q.\n", result)
			return result
		}
		log.Printf("Failed check: %v, next poll in %v...", err, delay)
		time.Sleep(delay)
	}
}

// PollChecker runs a check that returns a result. This is normally used to check an upstream
// repository for a release, or for go-images dependency flow status.
type PollChecker interface {
	// Check finds the string result associated with the check, or returns an error describing why
	// the result couldn't be found yet.
	Check() (string, error)
}

// CombinedOutput runs "git <args...>" in the given directory and returns the result.
func CombinedOutput(dir string, args ...string) (string, error) {
	return executil.CombinedOutput(executil.Dir(dir, "git", args...))
}

// RevParse runs "git rev-parse <rev>" and returns the result with whitespace trimmed.
func RevParse(dir, rev string) (string, error) {
	return executil.SpaceTrimmedCombinedOutput(executil.Dir(dir, "git", "rev-parse", rev))
}

// Show runs "git show <spec>" and returns the content.
func Show(dir, rev string) (string, error) {
	return CombinedOutput(dir, "show", rev)
}

// ShowQuietPretty runs "git show" with the given format and revision and returns the result.
// See https://git-scm.com/docs/git-show#_pretty_formats
func ShowQuietPretty(dir, format, rev string) (string, error) {
	return CombinedOutput(dir, "show", "--quiet", "--pretty=format:"+format, strings.TrimSpace(rev))
}

// GetSubmoduleCommitAtRev returns the commit hash of the submodule at the given
// revision. submoduleDir may be absolute or relative to dir.
func GetSubmoduleCommitAtRev(dir, submoduleDir, rev string) (string, error) {
	output, err := CombinedOutput(dir, "ls-tree", rev, submoduleDir)
	if err != nil {
		return "", fmt.Errorf("failed to get submodule commit at %q: %v", rev, err)
	}

	// Format, from Git docs: "<mode> SP <type> SP <object> TAB <file>"
	fields := strings.Fields(output)
	if len(fields) <= 2 {
		return "", fmt.Errorf("output from git ls-tree doesn't contain enough fields: %q", output)
	}
	return fields[2], nil
}

// CheckoutRevToTargetDir checks out the given path/subdir of the repository at
// the given revision into the target directory.
func CheckoutRevToTargetDir(dir, rev, path, targetDir string) error {
	data, err := executil.Dir(dir, "git", "archive", "--format=tar", rev, "--", path).Output()
	if err != nil {
		return fmt.Errorf("failed to create archive: %v", err)
	}
	r := tar.NewReader(bytes.NewReader(data))
	for {
		hdr, err := r.Next()
		if err != nil {
			if errors.Is(err, io.EOF) {
				break // End of archive.
			}
			return fmt.Errorf("failed to read next tar entry: %v", err)
		}
		name := hdr.Name
		if !filepath.IsLocal(name) {
			continue
		}
		// Don't create dirs when specified, just make them when necessary.
		if hdr.FileInfo().IsDir() {
			continue
		}
		if hdr.Typeflag != tar.TypeReg {
			continue
		}
		targetPath := filepath.Join(targetDir, name)
		if err := os.MkdirAll(filepath.Dir(targetPath), 0o755); err != nil {
			return fmt.Errorf("failed to create directory for %q: %v", targetPath, err)
		}
		f, err := os.Create(targetPath)
		if err != nil {
			return fmt.Errorf("failed to create file %q: %v", targetPath, err)
		}
		_, err = io.Copy(f, r)
		closeErr := f.Close()
		if err != nil {
			return fmt.Errorf("failed to copy data to %q: %v", targetPath, err)
		}
		if closeErr != nil {
			return fmt.Errorf("failed to close file %q: %v", targetPath, closeErr)
		}
	}
	return nil
}

// GetGlobalAuthor returns the author that a new Git commit would be written by, based (only!) on
// the current global Git settings.
func GetGlobalAuthor() (string, error) {
	name, err := getConfigGlobal("user.name")
	if err != nil {
		return "", err
	}
	email, err := getConfigGlobal("user.email")
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("%v <%v>", name, email), nil
}

// FetchRefCommit effectively runs "git fetch <remote> <ref>" and returns the commit hash.
func FetchRefCommit(dir, remote, ref string) (string, error) {
	output, err := executil.CombinedOutput(executil.Dir(dir, "git", "fetch", remote, "--porcelain", ref))
	if err != nil {
		return "", err
	}
	// https://git-scm.com/docs/git-fetch#_output
	fields := strings.Fields(output)
	if len(fields) != 4 {
		return "", fmt.Errorf("unexpected number of fields in fetch output: %q", output)
	}
	return fields[2], nil
}

// Run runs "git <args>" in the given directory, showing the command to the user in logs for
// diagnosability. Using this func helps make one-line Git commands readable.
func Run(dir string, args ...string) error {
	return executil.Run(executil.Dir(dir, "git", args...))
}

// Am runs "git -c am.threeWay=false am --whitespace=nowarn <args>" in the given directory.
//
// It explicitly disables am.threeWay to ensure the result doesn't depend on the user's Git
// configuration. This is important for internal processes like "git go-patch extract" that use "am"
// to compare patches: if the user has am.threeWay=true, three-way merge may silently resolve
// conflicts that would fail in CI (where am.threeWay is not set), causing a mismatch between local
// and CI behavior. See https://github.com/microsoft/go/issues/1233.
//
// It passes --whitespace=nowarn because trailing whitespace may be present in patch files. These
// warnings should be avoided when authoring each patch file, and emitting warnings at apply time
// would only be noisy.
func Am(dir string, args ...string) error {
	a := append([]string{"-c", "am.threeWay=false", "am", "--whitespace=nowarn"}, args...)
	return Run(dir, a...)
}

// NewTempGitRepo creates a gitRepo in temp storage. If desired, clean it up with AttemptDelete.
func NewTempGitRepo() (string, error) {
	gitDir, err := os.MkdirTemp("", "releasego-temp-git-*")
	if err != nil {
		return "", err
	}
	if err := executil.Run(exec.Command("git", "init", gitDir)); err != nil {
		return "", err
	}
	log.Printf("Created dir %#q to store temp Git repo.\n", gitDir)
	return gitDir, nil
}

func NewTempCloneRepo(src string) (string, error) {
	absSrc, err := filepath.Abs(src)
	if err != nil {
		return "", err
	}
	d, err := os.MkdirTemp("", "temp-clone-*")
	if err != nil {
		return "", err
	}
	if err := Run(d, "clone", "--no-checkout", absSrc, d); err != nil {
		return "", err
	}
	return d, nil
}

// AttemptDelete tries to delete the git dir. If an error occurs, log it, but this is not fatal.
// gitDir is expected to be in a temp dir, so it will be cleaned up later by the OS anyway.
func AttemptDelete(gitDir string) {
	if err := os.RemoveAll(gitDir); err != nil {
		log.Printf("Unable to clean up git repository directory %#q: %v\n", gitDir, err)
	}
}

func getConfigGlobal(key string) (string, error) {
	output, err := exec.Command("git", "config", "--global", key).CombinedOutput()
	if err != nil {
		return "", err
	}
	return strings.TrimSuffix(string(output), "\n"), nil
}
