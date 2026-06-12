// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package patch

import (
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/microsoft/go-infra/executil"
	"github.com/microsoft/go-infra/gitcmd"
)

// FilterVendorContent removes diff hunks for vendor directories from a patch's
// Content field (everything from "---" onwards). vendorPaths are path prefixes
// such as "src/vendor/" and "src/cmd/vendor/".
//
// The diffstat section between "---" and the first "diff --git" is dropped
// entirely because truncated paths make reliable filtering impractical.
// git am ignores the diffstat, so this has no functional impact.
func FilterVendorContent(content string, vendorPaths []string) string {
	lines := strings.Split(content, "\n")
	var filtered []string

	inDiffstat := false
	pastDiffstat := false
	skipSection := false

	for _, line := range lines {
		// The first "---" line begins the diffstat section.
		if !pastDiffstat && !inDiffstat && line == "---" {
			inDiffstat = true
			filtered = append(filtered, line)
			continue
		}

		if strings.HasPrefix(line, "diff --git ") {
			if inDiffstat {
				inDiffstat = false
				pastDiffstat = true
			}
			skipSection = isDiffForVendorPath(line, vendorPaths)
		}

		// The patch trailer ("-- \n2.45.0\n") is not part of any diff section.
		// Stop skipping when we reach it.
		if skipSection && line == "-- " {
			skipSection = false
		}

		if inDiffstat {
			continue
		}

		if skipSection {
			continue
		}

		filtered = append(filtered, line)
	}

	result := strings.Join(filtered, "\n")
	// Ensure the patch ends with a newline. When vendor diffs are the last
	// sections in the patch, stripping them can leave a file without a proper
	// trailing newline, which causes "git am" to report a corrupt patch.
	if !strings.HasSuffix(result, "\n") {
		result += "\n"
	}
	return result
}

// isDiffForVendorPath reports whether a "diff --git a/... b/..." line refers
// to a file under one of the given vendor path prefixes.
func isDiffForVendorPath(line string, vendorPaths []string) bool {
	for _, vp := range vendorPaths {
		if strings.Contains(line, " a/"+vp) || strings.Contains(line, " b/"+vp) {
			return true
		}
	}
	return false
}

// VendorPathsFromModDirs converts module directories (relative to the submodule
// root, e.g. "src", "src/cmd") into vendor path prefixes (e.g. "src/vendor/",
// "src/cmd/vendor/").
func VendorPathsFromModDirs(dirs []string) []string {
	paths := make([]string, len(dirs))
	for i, d := range dirs {
		paths[i] = d + "/vendor/"
	}
	return paths
}

// RunGoModVendor runs "go mod vendor" in each of the given module directories,
// stages the resulting changes, and optionally amends the current commit.
// moduleDirs are relative to submoduleDir (e.g. "src", "src/cmd").
// Set amend to true when applying patches as commits (ApplyModeCommits).
func RunGoModVendor(submoduleDir string, moduleDirs []string, amend bool) error {
	// Use the running Go toolchain's version as the temporary go directive.
	// This ensures compatibility: high enough for dependencies, but not
	// higher than what the toolchain can handle.
	runningVersion, err := runningGoVersion()
	if err != nil {
		return fmt.Errorf("detecting running Go version: %w", err)
	}

	for _, dir := range moduleDirs {
		if !filepath.IsLocal(dir) {
			return fmt.Errorf("invalid module directory %q: must be a relative path within the submodule", dir)
		}
		absDir := filepath.Join(submoduleDir, dir)

		// The go.mod may require a newer Go version than what's running (e.g.
		// go 1.27 when CI only has 1.24). Temporarily lower the go directive
		// so "go mod vendor" accepts the local toolchain, then restore it.
		// We parse go.mod as text to avoid running any go command that would
		// itself refuse to operate on a module requiring a newer Go.

		// Also find local replace targets whose go.mod may require a newer
		// Go version — "go mod vendor" checks all modules in the dependency
		// graph, including local replacements like ../../cryptobackend.
		dirsToEdit, err := collectLocalGoModDirs(absDir)
		if err != nil {
			return fmt.Errorf("collecting go.mod dirs for %s: %w", dir, err)
		}

		// Save original go directives and lower them all.
		type savedDirective struct {
			dir     string
			version string
		}
		var saved []savedDirective
		for _, d := range dirsToEdit {
			origVersion, err := readGoDirective(d)
			if err != nil {
				return fmt.Errorf("reading go directive in %s: %w", d, err)
			}
			saved = append(saved, savedDirective{d, origVersion})
			if err := goModEditGo(d, runningVersion); err != nil {
				return fmt.Errorf("lowering go directive in %s: %w", d, err)
			}
		}

		log.Printf("Running 'go mod vendor' in %s\n", absDir)
		// Set GOROOT to the submodule directory. The submodule is a Go source
		// tree, and modules like src/cmd (module "cmd") contain packages that
		// overlap with the stage0 Go's own GOROOT. Without this, "go mod vendor"
		// sees ambiguous imports (e.g. cmd/addr2line in both the module and the
		// external GOROOT). Pointing GOROOT at the submodule makes all cmd/*
		// packages resolve within the same tree.
		absSubmoduleDir, err := filepath.Abs(submoduleDir)
		if err != nil {
			return fmt.Errorf("resolving absolute path for submodule dir: %w", err)
		}
		vendorEnv := append(os.Environ(), "GOTOOLCHAIN=local", "GOROOT="+absSubmoduleDir)
		cmd := exec.Command("go", "mod", "vendor")
		cmd.Dir = absDir
		cmd.Env = vendorEnv
		vendorOut, vendorErr := cmd.CombinedOutput()

		// If vendor fails because go.mod is inconsistent after lowering the
		// go directive, run "go mod tidy" and retry. We don't tidy
		// unconditionally because it can fail on Go stdlib modules like
		// src/cmd where the module name collides with GOROOT packages.
		if vendorErr != nil && strings.Contains(string(vendorOut), "go mod tidy") {
			log.Printf("Running 'go mod tidy' in %s (go mod vendor requested it)\n", absDir)
			tidyCmd := exec.Command("go", "mod", "tidy")
			tidyCmd.Dir = absDir
			tidyCmd.Env = vendorEnv
			if err := executil.Run(tidyCmd); err != nil {
				for _, s := range saved {
					_ = goModEditGo(s.dir, s.version)
				}
				return fmt.Errorf("go mod tidy in %s: %w", dir, err)
			}

			log.Printf("Retrying 'go mod vendor' in %s\n", absDir)
			cmd = exec.Command("go", "mod", "vendor")
			cmd.Dir = absDir
			cmd.Env = vendorEnv
			vendorOut, vendorErr = cmd.CombinedOutput()
		}

		if vendorErr != nil {
			log.Printf("go mod vendor output:\n%s", vendorOut)
		}

		// Restore all original go directives regardless of whether vendor succeeded.
		for _, s := range saved {
			if err := goModEditGo(s.dir, s.version); err != nil {
				return fmt.Errorf("restoring go directive to %s in %s: %w", s.version, s.dir, err)
			}
		}

		if vendorErr != nil {
			return fmt.Errorf("go mod vendor in %s: %w", dir, vendorErr)
		}

		// Fix modules.txt: "go mod vendor" recorded the lowered go versions
		// for local replace targets. Now that we've restored the originals in
		// their go.mod files, modules.txt must match to pass vendor checks.
		moduleVersions := make(map[string]string)
		for _, s := range saved {
			if s.dir == absDir {
				continue // Main module's version isn't in modules.txt.
			}
			modPath, err := readModulePath(s.dir)
			if err != nil {
				return fmt.Errorf("reading module path from %s: %w", s.dir, err)
			}
			moduleVersions[modPath] = s.version
		}
		if len(moduleVersions) > 0 {
			vendorTxt := filepath.Join(absDir, "vendor", "modules.txt")
			if err := fixModulesTxtGoVersions(vendorTxt, moduleVersions); err != nil {
				return fmt.Errorf("fixing modules.txt go versions in %s: %w", dir, err)
			}
		}
	}

	if err := gitcmd.Run(submoduleDir, "add", "-A"); err != nil {
		return fmt.Errorf("staging vendor changes: %w", err)
	}
	if amend {
		if err := gitcmd.Run(submoduleDir, "commit", "--amend", "--no-edit"); err != nil {
			return fmt.Errorf("amending commit with vendor changes: %w", err)
		}
	}
	return nil
}

// goModEditGo runs "go mod edit -go=<version>" in the given directory with
// GOTOOLCHAIN=local to avoid toolchain download attempts.
func goModEditGo(dir, version string) error {
	cmd := exec.Command("go", "mod", "edit", "-go="+version)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GOTOOLCHAIN=local")
	return executil.Run(cmd)
}

// runningGoVersion returns the major.minor version of the running Go toolchain
// (e.g. "1.25"). It parses the output of "go env GOVERSION" which returns
// something like "go1.25.0".
func runningGoVersion() (string, error) {
	cmd := exec.Command("go", "env", "GOVERSION")
	cmd.Env = append(os.Environ(), "GOTOOLCHAIN=local")
	out, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("go env GOVERSION: %w", err)
	}
	// Output is like "go1.25.0\n". Strip "go" prefix and trailing whitespace.
	version := strings.TrimSpace(strings.TrimPrefix(string(out), "go"))
	if version == "" {
		return "", fmt.Errorf("empty version from go env GOVERSION")
	}
	// Use major.minor only (e.g. "1.25" from "1.25.0") to match go.mod convention.
	parts := strings.SplitN(version, ".", 3)
	if len(parts) >= 2 {
		return parts[0] + "." + parts[1], nil
	}
	return version, nil
}

// collectLocalGoModDirs returns the absolute paths of directories containing
// go.mod files that need their go directive lowered before "go mod vendor".
// This includes the module directory itself plus any local replace targets
// (replace directives with relative filesystem paths).
func collectLocalGoModDirs(absModDir string) ([]string, error) {
	dirs := []string{absModDir}

	replaceDirs, err := localReplaceDirs(absModDir)
	if err != nil {
		return nil, err
	}
	dirs = append(dirs, replaceDirs...)
	return dirs, nil
}

// localReplaceDirs parses go.mod as text and returns absolute paths for any
// local (filesystem) replace targets. These are replace directives whose
// replacement path starts with "./" or "../".
func localReplaceDirs(absModDir string) ([]string, error) {
	data, err := os.ReadFile(filepath.Join(absModDir, "go.mod"))
	if err != nil {
		return nil, err
	}

	var dirs []string
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "replace ") {
			continue
		}
		// replace directives: "replace module => path" or "replace module => module version"
		parts := strings.Split(line, "=>")
		if len(parts) != 2 {
			continue
		}
		target := strings.TrimSpace(parts[1])
		// Local replacements start with "./" or "../"
		if !strings.HasPrefix(target, "./") && !strings.HasPrefix(target, "../") {
			continue
		}
		absTarget := filepath.Join(absModDir, target)
		// Only include if a go.mod exists there.
		if _, err := os.Stat(filepath.Join(absTarget, "go.mod")); err != nil {
			continue
		}
		dirs = append(dirs, absTarget)
	}
	return dirs, nil
}

// readGoDirective reads the go directive from go.mod by parsing the file as
// text. This avoids running "go mod edit -json" which itself may refuse to
// operate on a go.mod requiring a newer Go version than the local toolchain.
func readGoDirective(dir string) (string, error) {
	data, err := os.ReadFile(filepath.Join(dir, "go.mod"))
	if err != nil {
		return "", err
	}
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "go ") {
			version := strings.TrimSpace(strings.TrimPrefix(line, "go "))
			if version != "" {
				return version, nil
			}
		}
	}
	return "", fmt.Errorf("no go directive found in %s/go.mod", dir)
}

// readModulePath reads the module path from go.mod in the given directory.
func readModulePath(dir string) (string, error) {
	data, err := os.ReadFile(filepath.Join(dir, "go.mod"))
	if err != nil {
		return "", err
	}
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "module ") {
			return strings.TrimSpace(strings.TrimPrefix(line, "module ")), nil
		}
	}
	return "", fmt.Errorf("no module directive found in %s/go.mod", dir)
}

// fixModulesTxtGoVersions updates vendor/modules.txt to reflect restored go
// directive versions for local replace targets. When we temporarily lower go
// directives for "go mod vendor" compatibility, the generated modules.txt
// records the lowered versions. After restoring go.mod, we fix modules.txt
// so post-build vendor checks pass.
func fixModulesTxtGoVersions(modulesTxtPath string, moduleVersions map[string]string) error {
	data, err := os.ReadFile(modulesTxtPath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}

	lines := strings.Split(string(data), "\n")
	var currentModule string
	changed := false
	for i, line := range lines {
		if strings.HasPrefix(line, "# ") {
			// Extract module path from "# module version [=> replacement]"
			fields := strings.Fields(line)
			if len(fields) >= 2 {
				currentModule = fields[1]
			}
		}
		if strings.HasPrefix(line, "## explicit") && currentModule != "" {
			if version, ok := moduleVersions[currentModule]; ok {
				newLine := "## explicit; go " + version
				if lines[i] != newLine {
					lines[i] = newLine
					changed = true
				}
				currentModule = "" // Only fix once per module.
			}
		}
	}

	if !changed {
		return nil
	}
	return os.WriteFile(modulesTxtPath, []byte(strings.Join(lines, "\n")), 0o644)
}
