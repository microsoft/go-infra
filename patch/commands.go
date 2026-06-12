// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package patch

import (
	"bufio"
	"fmt"
	"io"
	"strings"
)

const (
	// CommandPrefix is the prefix used in commit messages to identify git-go-patch commands.
	CommandPrefix = "github.com/microsoft/go-infra/cmd/git-go-patch command: "
	// PatchNumberCommand is the command to set the patch number.
	PatchNumberCommand = "patch number "
	// AutoVendorPrefix is the command prefix for auto-vendor patches.
	AutoVendorPrefix = "auto vendor"
)

// ReadPatchCommands reads a patch file's subject and returns all commands found,
// with CommandPrefix trimmed off.
func ReadPatchCommands(r io.Reader) ([]string, error) {
	var cmds []string
	scanner := bufio.NewScanner(r)
	for scanner.Scan() {
		t := scanner.Text()
		if t == "---" {
			// Patch is done: stop reading. Technically, "---" could occur inside the commit
			// message, so we might be giving up early. But even "git format-patch" and "git am"
			// don't round-trip "---" ("format-patch" doesn't escape it, "am" cuts off the message),
			// so don't worry about it here.
			break
		}
		if after, found := strings.CutPrefix(t, CommandPrefix); found {
			cmds = append(cmds, after)
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	return cmds, nil
}

// ScanAutoVendorPatches pre-scans all patch files for auto-vendor commands and
// returns a map from patch file path to the list of module directories that need
// "go mod vendor" after application.
func ScanAutoVendorPatches(config *FoundConfig) (map[string][]string, error) {
	result := make(map[string][]string)
	err := WalkGoPatches(config, func(file string) error {
		p, err := ReadFile(file)
		if err != nil {
			return err
		}
		cmds, err := ReadPatchCommands(strings.NewReader(p.Subject))
		if err != nil {
			return err
		}
		for _, cmd := range cmds {
			if after, found := strings.CutPrefix(cmd, AutoVendorPrefix); found {
				dirs := strings.Fields(after)
				if len(dirs) == 0 {
					return fmt.Errorf("auto vendor command requires module directories (e.g. %q) in %q", "auto vendor src src/cmd", file)
				}
				result[file] = dirs
			}
		}
		return nil
	})
	return result, err
}
