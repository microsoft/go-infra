// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package main

import (
	"bufio"
	"flag"
	"fmt"
	"io"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/microsoft/go-infra/executil"
	"github.com/microsoft/go-infra/patch"
	"github.com/microsoft/go-infra/stringutil"
	"github.com/microsoft/go-infra/subcmd"
)

func init() {
	subcommands = append(subcommands, subcmd.Option{
		Name:    "extract",
		Summary: "Format each new commit in the submodule as a patch file.",
		Description: `

This command figures out which commits are new by checking for commits in HEAD since the given
commit. If no commit is given, the commit recorded by "apply" is used. If the given commit is not an
ancestor of the HEAD commit, "extract" formats patches for each commit until a common ancestor of HEAD
and the given commit. (See "git format-patch" documentation for "<since>".)

extract uses "git format-patch" internally, passing additional arguments to reduce the amount of
non-repeatable data in the resulting patch file.

extract searches inside each commit description for lines that contain (only) a command.
Each command starts with "` + commandPrefix + `", then:

- "` + patchNumberCommand + `<x>"
  Set the number of the patch to x. Subsequent patches are numbered x+1, x+2, and so on. x must be
  >= the number this patch would be assigned without this command.

  Consider using this if you are maintaining the same patches on multiple branches and there are
  distinct groups of patches. Making the first patch in each group start at a consistent number can
  help to avoid unnecessary filename conflicts when porting changes between branches.
` + repoRootSearchDescription,
		Handle: handleExtract,
	})
}

const commandPrefix = "github.com/microsoft/go-infra/cmd/git-go-patch command: "
const patchNumberCommand = "patch number "

func handleExtract(p subcmd.ParseFunc) error {
	sinceFlag := flag.String("since", "", "The commit or ref to begin formatting patches at. If nothing is specified, use the last commit recorded by 'apply'.")
	keepTemp := flag.Bool("w", false, "Keep the temporary working directory used by the patch rewrite process, rather than cleaning it up.")

	if err := p(); err != nil {
		return err
	}

	rootDir, err := findOuterRepoRoot()
	if err != nil {
		return err
	}

	goDir := filepath.Join(rootDir, "go")
	patchDir := filepath.Join(rootDir, "patches")

	since := *sinceFlag
	if since == "" {
		since, err = readStatusFile(getPrePatchStatusFilePath(rootDir))
		if err != nil {
			return err
		}
	}

	// Emit the patch files into a scratch directory for now. We will process them a bit, and
	// later overwrite the contents of the patchDir once we know that the patch files are valid.
	tmpPatchDir, err := os.MkdirTemp("", "extracted-patches-*")
	if err != nil {
		return err
	}
	if *keepTemp {
		log.Printf("Created dir %#q to process patch files.\n", tmpPatchDir)
	} else {
		log.Printf("Created temp dir %#q to process patch files. The dir will be deleted when patch processing completes.\n", tmpPatchDir)
		defer func() {
			if err := os.RemoveAll(tmpPatchDir); err != nil {
				log.Printf("Unable to clean up temp directory %#q: %v\n", tmpPatchDir, err)
			}
		}()
	}
	tmpRawDir := filepath.Join(tmpPatchDir, "raw")
	tmpRenameDir := filepath.Join(tmpPatchDir, "rename")

	cmd := exec.Command(
		"git",
		"format-patch",

		// Remove default signature, which includes the Git version.
		"--signature=",
		// Use "From 0000000" instead of "From abc123f" in the patch file. A new commit hash is
		// generated each time the patches are applied, and including it in the patch text would
		// make the process less repeatable.
		"--zero-commit",
		// Remove "[PATCH 1/3]" from the patch file content. Avoid the reference to the total
		// number of patch files so earlier patch files don't change when a new one is appended.
		"--no-numbered",
		// Emit the patch files in the working directory.
		"-o", tmpRawDir,

		since,
	)
	cmd.Dir = goDir

	if err := executil.Run(cmd); err != nil {
		return err
	}

	// Start numbering patches at 1 (0001).
	n := 1
	// Git has extracted the commits and given them sequential numbers in their filenames. Here,
	// renumber the patch files with our own rules.
	if err := patch.WalkPatches(tmpRawDir, func(path string) error {
		cmds, err := readPatchCommands(path)
		if err != nil {
			return err
		}
		for _, cmd := range cmds {
			if after, found := stringutil.CutPrefix(cmd, patchNumberCommand); found {
				num, err := strconv.Atoi(after)
				if err != nil {
					return fmt.Errorf("malformed patch number command arg %q in %#q: %w", after, path, err)
				}
				if num >= n {
					n = num
				} else {
					return fmt.Errorf("patch number command arg %v too small, expected at least %v in %#q", num, n, path)
				}
			} else {
				return fmt.Errorf("command %#q is not recognized in patch %#q", cmd, path)
			}
		}

		if n > 9999 {
			return fmt.Errorf("rearranged patch number %v exceeds max 4-digit int used by patch naming convention: %#q", n, path)
		}

		// Replace patch number (0001) from patch filename (0001-Add-good-code.patch) with n.
		_, after, found := strings.Cut(filepath.Base(path), "-")
		if !found {
			return fmt.Errorf("no number prefix found in %#q", path)
		}
		newName := fmt.Sprintf("%04v-%v", strconv.Itoa(n), after)
		if err := copyFile(filepath.Join(tmpRenameDir, newName), path); err != nil {
			return fmt.Errorf("unable to rename %#q: %w", path, err)
		}

		n++
		return nil
	}); err != nil {
		return err
	}

	// Delete all old patches so if any commit descriptions have been changed, we don't end up
	// with two copies of those patch files with slightly different names.
	if err := patch.WalkGoPatches(rootDir, func(path string) error {
		return os.Remove(path)
	}); err != nil {
		return err
	}

	// Move all patch files from the temp dir to the final dir.
	if err := patch.WalkPatches(tmpRenameDir, func(path string) error {
		filename := filepath.Base(path)
		log.Printf("Moving patch to destination: %#q\n", filename)
		return copyFile(filepath.Join(patchDir, filename), path)
	}); err != nil {
		return err
	}

	log.Printf("Extracted patch files from %#q into %#q\n", goDir, patchDir)
	return nil
}

// readPatchCommands reads the given patch file's header and returns all potential commands, with
// commandPrefix trimmed off.
func readPatchCommands(path string) ([]string, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()

	var cmds []string

	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		if err := scanner.Err(); err != nil {
			return nil, err
		}
		t := scanner.Text()
		if t == "---" {
			// Header is done: stop reading. Technically, "---" could occur inside the commit
			// message, so we might be giving up early. But even "git format-patch" and "git am"
			// don't round-trip "---" ("format-patch" doesn't escape it, "am" cuts off the message),
			// so don't worry about it here.
			break
		}
		if after, found := stringutil.CutPrefix(t, commandPrefix); found {
			cmds = append(cmds, after)
		}
	}
	return cmds, nil
}

func copyFile(dst, src string) error {
	if err := os.MkdirAll(filepath.Dir(dst), os.ModePerm); err != nil {
		return err
	}

	s, err := os.Open(src)
	if err != nil {
		return err
	}
	defer s.Close()

	d, err := os.Create(dst)
	if err != nil {
		return err
	}
	defer d.Close()

	if _, err := io.Copy(d, s); err != nil {
		return err
	}
	return d.Close()
}
