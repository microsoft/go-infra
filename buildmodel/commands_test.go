// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package buildmodel

import (
	"flag"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/microsoft/go-infra/buildmodel/buildassets"
	"github.com/microsoft/go-infra/stringutil"
)

func TestSubmitUpdatePRSkips(t *testing.T) {
	t.Setenv("BUILD_BUILDID", "123")
	for _, tt := range []struct {
		name           string
		branch         string
		versions       string
		manifest       string
		wantErr        bool
		setBuildNumber bool
	}{
		{"unmatched version", "release-branch.go1.42", "{}", "{}", false, true},
		{"number disabled", "release-branch.go1.42", "{}", "{}", false, false},
		{"unmapped branch", "feature/test", "{}", "{}", false, true},
		{"literal percent escape", "feature/%0Atest", "{}", "{}", false, true},
		{"invalid versions", "release-branch.go1.42", "{", "{}", true, true},
		{"invalid manifest", "release-branch.go1.42", "{}", "{", true, true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			origin := filepath.Join(dir, "origin")
			versionsDir := filepath.Join(origin, "src", "microsoft")
			if err := os.MkdirAll(versionsDir, 0o755); err != nil {
				t.Fatal(err)
			}
			for path, content := range map[string]string{
				filepath.Join(versionsDir, "versions.json"): tt.versions,
				filepath.Join(origin, "manifest.json"):      tt.manifest,
			} {
				if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			for _, args := range [][]string{
				{"init", "-b", "microsoft/nightly"},
				{"add", "."},
				{"-c", "user.name=Test", "-c", "user.email=test@example.com", "commit", "-m", "Initial files"},
			} {
				cmd := exec.Command("git", args...)
				cmd.Dir = origin
				if out, err := cmd.CombinedOutput(); err != nil {
					t.Fatalf("git %v: %v\n%s", args, err, out)
				}
			}
			assetPath := filepath.Join(dir, "assets.json")
			if err := stringutil.WriteJSONFile(assetPath, &buildassets.BuildAssets{
				Branch: tt.branch, Version: "1.42.1",
			}); err != nil {
				t.Fatal(err)
			}

			oldFlags := flag.CommandLine
			flag.CommandLine = flag.NewFlagSet(tt.name, flag.ContinueOnError)
			defer func() { flag.CommandLine = oldFlags }()
			f := BindPRFlags()
			originPath := filepath.ToSlash(origin)
			if filepath.VolumeName(origin) != "" {
				originPath = "/" + originPath
			}
			*f.origin = (&url.URL{Scheme: "file", Path: originPath}).String()
			*f.tempGitDir = filepath.Join(dir, "work")
			*f.buildAssetJSON = assetPath
			*f.skipDockerfiles = true
			*f.dryRun = true
			*f.setBuildNumber = tt.setBuildNumber
			*f.SetVariablePRNumber = "PRNumber"
			*f.SetVariableUpToDateCommit = "Commit"

			if tt.name == "unmapped branch" {
				*f.skipDockerfiles = false
				t.Setenv("PATH", "")
			}

			output, err := os.Create(filepath.Join(dir, "stdout"))
			if err != nil {
				t.Fatal(err)
			}
			defer output.Close()
			oldStdout := os.Stdout
			os.Stdout = output
			defer func() { os.Stdout = oldStdout }()
			err = SubmitUpdatePR(f)
			os.Stdout = oldStdout
			if tt.name == "unmapped branch" {
				if _, err := os.Stat(*f.tempGitDir); !os.IsNotExist(err) {
					t.Fatalf("unmapped branch created a work directory or stat failed: %v", err)
				}
			}
			if (err != nil) != tt.wantErr {
				t.Fatalf("SubmitUpdatePR() error = %v, wantErr %v", err, tt.wantErr)
			}
			data, err := os.ReadFile(output.Name())
			if err != nil {
				t.Fatal(err)
			}
			got := string(data)
			wantNumber := "##vso[build.updatebuildnumber]123_" +
				strings.NewReplacer("/", "_", "%", "%AZP25").Replace(tt.branch) + "_1.42.1\n"
			if strings.Contains(got, wantNumber) != tt.setBuildNumber {
				t.Errorf("unexpected build number output:\n%s", got)
			}
			for _, want := range []string{
				"##vso[task.setvariable variable=PRNumber]nil\n",
				"##vso[task.setvariable variable=Commit]nil\n",
			} {
				if strings.Contains(got, want) == tt.wantErr {
					t.Errorf("unexpected skip output for %q:\n%s", want, got)
				}
			}
		})
	}
}
