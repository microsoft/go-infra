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
	"time"

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

const (
	commandPrefix      = "github.com/microsoft/go-infra/cmd/git-go-patch command: "
	patchNumberCommand = "patch number "
)

func handleExtract(p subcmd.ParseFunc) error {
	sinceFlag := flag.String("since", "", "The commit or ref to begin formatting patches at. If nothing is specified, use the last commit recorded by 'apply'.")
	verbatim := flag.Bool("verbatim", false, "Extract every patch even if rewriting it results in only spurious changes.")
	keepTemp := flag.Bool("w", false, "Keep the temporary working directory used by the patch rewrite process, rather than cleaning it up.")

	if err := p(); err != nil {
		return err
	}

	// Keep track of time. Finding spurious changes takes a surprisingly long time, and devs should
	// be able to make an informed decision about '-verbatim'.
	var totalStopwatch, matchingStopwatch stopwatch
	totalStopwatch.Start()

	config, err := loadConfig()
	if err != nil {
		return err
	}
	rootDir, goDir := config.FullProjectRoots()
	patchDir := filepath.Join(rootDir, config.PatchesDir)

	since := *sinceFlag
	if since == "" {
		since, err = readStatusFile(config.FullPrePatchStatusFilePath())
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

	if err := os.MkdirAll(tmpRenameDir, os.ModePerm); err != nil {
		return fmt.Errorf("unable to create temp dir for patch renames: %v", err)
	}

	cmd := exec.Command(
		"git",
		"format-patch",

		// Set the minimum abbreviation level to a certain value to avoid user-specific defaults,
		// which may change due to Git version or user configuration.
		"--abbrev=14",
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

	// Set up a checker that will determine which patch files actually need to change.
	var matcher *patch.MatchCheckRepo
	if !*verbatim {
		matchingStopwatch.Start()
		matcher, err = patch.NewMatchCheckRepo(goDir, since, patchDir)
		if err != nil {
			return failSuggestVerbatim("failed to create patch checking context", err)
		}
		if !*keepTemp {
			defer matcher.AttemptDelete()
		}
		matchingStopwatch.Stop()
	}

	// Start numbering patches at 1 (0001).
	n := 1
	if err := patch.WalkPatches(tmpRawDir, func(path string) error {
		p, err := patch.ReadFile(path)
		if err != nil {
			return err
		}

		if config.ExtractAsAuthor != "" {
			p.FromAuthor = config.ExtractAsAuthor
		}

		subjectReader := strings.NewReader(p.Subject)
		cmds, err := readPatchCommands(subjectReader)
		if err != nil {
			return err
		}
		for _, cmd := range cmds {
			// Git has extracted the commits and given them sequential numbers in their filenames.
			// Here, renumber the patch files with our own rules.
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
		writeNewPatch := true

		// Now that we're done modifying p, see if it has any effective differences vs. the old
		// patch with the same header.
		if matcher != nil {
			matchingStopwatch.Start()
			matchPath, err := matcher.CheckedApply(path, p)
			if err != nil {
				return failSuggestVerbatim("failed to check patch for changes", err)
			}
			if matchPath != "" {
				// Copy the old file: we know the content is the same, but the filename might not
				// be. (In particular, the patch number.)
				if err := copyFile(filepath.Join(tmpRenameDir, newName), matchPath); err != nil {
					return err
				}
				writeNewPatch = false
			}
			matchingStopwatch.Stop()
		}

		if writeNewPatch {
			modifiedFile, err := os.Create(filepath.Join(tmpRenameDir, newName))
			if err != nil {
				return err
			}
			defer modifiedFile.Close()

			if _, err := modifiedFile.WriteString(p.String()); err != nil {
				return fmt.Errorf("unable to write patch to %#q: %v", path, err)
			}
			if err := modifiedFile.Close(); err != nil {
				return fmt.Errorf("unable to close patch %#q: %v", path, err)
			}
		}

		n++
		return nil
	}); err != nil {
		return err
	}

	// Delete all old patches so if any commit descriptions have been changed, we don't end up
	// with two copies of those patch files with slightly different names.
	if err := patch.WalkGoPatches(config, func(path string) error {
		return os.Remove(path)
	}); err != nil {
		return err
	}

	// Move all patch files from the temp dir to the final dir.
	log.Printf("Moving patches from %#q to destination %#q\n", tmpRenameDir, patchDir)
	if err := patch.WalkPatches(tmpRenameDir, func(path string) error {
		dstPath := filepath.Join(patchDir, filepath.Base(path))
		err := copyFile(dstPath, path)
		if err != nil {
			return fmt.Errorf("failed to copy patch %#q to %#q: %v", path, dstPath, err)
		}
		return nil
	}); err != nil {
		return err
	}

	totalStopwatch.Stop()
	log.Printf("Extracted patch files from %#q into %#q in %v\n", goDir, patchDir, totalStopwatch.ElapsedMillis())
	if matcher != nil {
		log.Printf(
			"Of that time, reducing spurious changes took %v. "+
				"If this is a burden, consider using '-verbatim' mode to allow spurious changes but take ~%v.\n",
			matchingStopwatch.ElapsedMillis(),
			totalStopwatch.ElapsedMillis()-matchingStopwatch.ElapsedMillis())
	}
	return nil
}

// failSuggestVerbatim creates an error message that suggests using '-verbatim' as an alternative.
func failSuggestVerbatim(description string, err error) error {
	return fmt.Errorf("%v; use '-verbatim' or fix the underlying issue: %v", description, err)
}

// readPatchCommands reads the given patch file's header and returns all potential commands, with
// commandPrefix trimmed off.
func readPatchCommands(r io.Reader) ([]string, error) {
	var cmds []string
	scanner := bufio.NewScanner(r)
	for scanner.Scan() {
		if err := scanner.Err(); err != nil {
			return nil, err
		}
		t := scanner.Text()
		if t == "---" {
			// Patch is done: stop reading. Technically, "---" could occur inside the commit
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

type stopwatch struct {
	elapsed time.Duration
	start   time.Time
}

func (s *stopwatch) Start() {
	s.start = time.Now()
}

func (s *stopwatch) Stop() {
	s.elapsed += time.Since(s.start)
}

func (s *stopwatch) ElapsedMillis() time.Duration {
	return s.elapsed.Round(time.Millisecond)
}
