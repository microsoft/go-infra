// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package sync

import (
	"fmt"
	"net/url"
	"os/exec"
	"path"
	"strings"

	"github.com/microsoft/go-infra/gitcmd"
	"github.com/microsoft/go-infra/gitpr"
)

func syncDocs(dir, upstream string, branch *gitpr.SyncPRRefSet) error {
	paths := []string{"README.md", "SUPPORT.md", "SECURITY.md", "eng/doc"}
	source := branch.UpstreamLocalSyncTarget()
	tree, err := gitcmd.CombinedOutput(dir, append([]string{"ls-tree", "-r", "-z", source, "--"}, paths...)...)
	if err != nil {
		return err
	}
	remote, err := gitpr.ParseRemoteURL(upstream)
	if err != nil {
		return err
	}

	// Remove the target's matching files first, including docs deleted or renamed on main.
	// Use Git to materialize files so a target symlink cannot redirect writes outside the repo.
	if _, err := gitcmd.CombinedOutput(dir, append([]string{"rm", "-r", "--ignore-unmatch", "--"}, paths...)...); err != nil {
		return err
	}
	for entry := range strings.SplitSeq(tree, "\x00") {
		if entry == "" {
			continue
		}
		metadata, name, ok := strings.Cut(entry, "\t")
		fields := strings.Fields(metadata)
		if !ok || len(fields) != 3 || (fields[0] != "100644" && fields[0] != "100755") {
			return fmt.Errorf("cannot sync non-regular documentation file: %q", entry)
		}
		content, err := gitcmd.Show(dir, fields[2])
		if err != nil {
			return err
		}
		// Assets such as JSON and images must remain byte-for-byte copies.
		if strings.EqualFold(path.Ext(name), ".md") {
			sourceURL := &url.URL{
				Scheme: "https",
				Host:   "github.com",
				Path:   "/" + remote.GetOwnerSlashRepo() + "/blob/" + branch.UpstreamName + "/" + name,
			}
			content = fmt.Sprintf(
				"<!-- This document is maintained in %s and automatically copied to %s, DO NOT EDIT. -->\n\n",
				sourceURL, branch.Name) + content
		}
		hash := exec.Command("git", "hash-object", "-w", "--stdin")
		hash.Dir = dir
		hash.Stdin = strings.NewReader(content)
		blob, err := combinedOutput(hash)
		if err != nil {
			return err
		}
		if _, err := gitcmd.CombinedOutput(dir, "update-index", "--add", "--cacheinfo",
			fields[0]+","+strings.TrimSpace(blob)+","+name); err != nil {
			return err
		}
		if _, err := gitcmd.CombinedOutput(dir, "checkout-index", "-f", "--", name); err != nil {
			return err
		}
	}
	return nil
}
