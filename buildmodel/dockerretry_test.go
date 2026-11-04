// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package buildmodel

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/microsoft/go-infra/buildmodel/buildassets"
	"github.com/microsoft/go-infra/buildmodel/dockerversions"
	"github.com/microsoft/go-infra/stringutil"
)

func TestDockerUpdateStateConcurrentChanges(t *testing.T) {
	for _, tt := range []struct {
		name    string
		version string
		change  func(*dockerversions.MajorMinorVersion)
	}{
		{"newer patch", "1.42.1", func(v *dockerversions.MajorMinorVersion) { v.Version = "1.42.2" }},
		{"newer revision", "1.42.1", func(v *dockerversions.MajorMinorVersion) { v.Revision = "2" }},
		{"same version different URL", "1.42.1", func(v *dockerversions.MajorMinorVersion) { v.Arches["amd64"].URL = "concurrent" }},
		{"same version different checksum", "1.42.1", func(v *dockerversions.MajorMinorVersion) { v.Arches["amd64"].SHA256 = "concurrent" }},
		{"main nightly different artifacts", "main", func(v *dockerversions.MajorMinorVersion) { v.Arches["amd64"].SHA256 = "concurrent" }},
	} {
		t.Run(tt.name, func(t *testing.T) {
			assets := &buildassets.BuildAssets{Version: tt.version, Branch: "main"}
			key := assets.GetDockerRepoVersionsKey()
			versions := dockerversions.Versions{key: {
				Version: tt.version,
				Arches:  map[string]*dockerversions.Arch{"amd64": {URL: "original", SHA256: "original"}},
			}}
			dir := dockerRetryTestRepo(t, versions)
			var state dockerUpdateState
			if err := state.Check(dir, assets); err != nil {
				t.Fatal(err)
			}
			tt.change(versions[key])
			writeDockerRetryVersions(t, dir, versions)
			for range 2 {
				if err := state.Check(dir, assets); err == nil || !strings.Contains(err.Error(), "stale Docker update") {
					t.Fatalf("want stale concurrent update error, got %v", err)
				}
			}
		})
	}
}

func TestDockerUpdateStateAcceptsSafeRetries(t *testing.T) {
	for _, partialPush := range []bool{false, true} {
		t.Run(map[bool]string{false: "unchanged slot", true: "intended update already pushed"}[partialPush], func(t *testing.T) {
			assets := &buildassets.BuildAssets{
				Version: "1.42.1",
				Arches: []*dockerversions.Arch{{
					Env: &dockerversions.ArchEnv{GOOS: "linux", GOARCH: "amd64"},
					URL: "incoming", SHA256: "incoming",
				}},
			}
			versions := dockerversions.Versions{
				"1.42": {Version: "1.42.0", Arches: map[string]*dockerversions.Arch{"amd64": {URL: "old"}}},
				"1.41": {Version: "1.41.0"},
			}
			dir := dockerRetryTestRepo(t, versions)
			var state dockerUpdateState
			if err := state.Check(dir, assets); err != nil {
				t.Fatal(err)
			}
			if partialPush {
				if err := UpdateVersions(assets, versions); err != nil {
					t.Fatal(err)
				}
			}
			versions["1.41"].Version = "1.41.2"
			versions["1.42"].PreferredMajor = true
			versions["1.42"].PreferredMinor = true
			versions["1.42"].Variants = []string{"new-variant"}
			writeDockerRetryVersions(t, dir, versions)
			if err := state.Check(dir, assets); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestDockerUpdateStateDoesNotMutateAssets(t *testing.T) {
	assets := &buildassets.BuildAssets{
		Version: "1.42.1",
		Arches: []*dockerversions.Arch{{
			Env: &dockerversions.ArchEnv{GOOS: "linux", GOARCH: "arm", GOARM: "6"},
			URL: "incoming",
		}},
	}
	dir := dockerRetryTestRepo(t, dockerversions.Versions{"1.42": {Version: "1.42.0"}})
	before, err := json.Marshal(assets)
	if err != nil {
		t.Fatal(err)
	}
	var state dockerUpdateState
	if err := state.Check(dir, assets); err != nil {
		t.Fatal(err)
	}
	after, err := json.Marshal(assets)
	if err != nil {
		t.Fatal(err)
	}
	if string(before) != string(after) {
		t.Fatalf("guard mutated assets: before %s, after %s", before, after)
	}
	var versions dockerversions.Versions
	if err := stringutil.ReadJSONFile(filepath.Join(dir, "src", "microsoft", "versions.json"), &versions); err != nil {
		t.Fatal(err)
	}
	if versions["1.42"].Version != "1.42.0" {
		t.Fatal("guard wrote versions.json")
	}
	if err := UpdateVersions(assets, versions); err != nil {
		t.Fatal(err)
	}
	writeDockerRetryVersions(t, dir, versions)
	if err := state.Check(dir, assets); err != nil {
		t.Fatalf("guard did not predict normalized ARM artifacts: %v", err)
	}
}

func TestDockerUpdateStateNewSlot(t *testing.T) {
	assets := &buildassets.BuildAssets{Version: "1.42.1"}
	versions := dockerversions.Versions{"1.41": {Version: "1.41.0"}}
	dir := dockerRetryTestRepo(t, versions)
	var state dockerUpdateState
	if err := state.Check(dir, assets); err != nil {
		t.Fatal(err)
	}
	if err := state.Check(dir, assets); err != nil {
		t.Fatal(err)
	}
	if err := UpdateVersions(assets, versions); err != nil {
		t.Fatal(err)
	}
	writeDockerRetryVersions(t, dir, versions)
	if err := state.Check(dir, assets); err != nil {
		t.Fatal(err)
	}
	versions["1.42"].Version = "1.42.2"
	writeDockerRetryVersions(t, dir, versions)
	if err := state.Check(dir, assets); err == nil {
		t.Fatal("accepted concurrent creation of newer slot")
	}
}

func TestDockerUpdateStateUnmatchedSlot(t *testing.T) {
	versions := dockerversions.Versions{"1.40": {Version: "1.40.0"}}
	dir := dockerRetryTestRepo(t, versions)
	assets := &buildassets.BuildAssets{Version: "1.42.1"}
	var state dockerUpdateState
	if err := state.Check(dir, assets); err != nil {
		t.Fatalf("guard should defer unmatched slot handling: %v", err)
	}
	if err := UpdateVersions(assets, versions); !errors.Is(err, ErrNoMajorMinorUpgradeMatch) {
		t.Fatalf("want ErrNoMajorMinorUpgradeMatch, got %v", err)
	}
}

func TestDockerUpdateStateRejectsInitialDowngrade(t *testing.T) {
	for _, tt := range []struct {
		name, incoming, current, revision string
	}{
		{"patch", "1.42.1", "1.42.2", "1"},
		{"revision", "1.42.1-9", "1.42.1", "10"},
		{"default revision", "1.42.1", "1.42.1", "2"},
		{"release candidate", "1.42rc9", "1.42.0rc10", "1"},
		{"beta", "1.42beta1", "1.42.0beta2", "1"},
		{"beta before release candidate", "1.42beta2", "1.42.0rc1", "1"},
		{"release candidate revision", "1.42rc1-9", "1.42.0rc1", "10"},
		{"beta revision", "1.42beta1-9", "1.42.0beta1", "10"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			assets := &buildassets.BuildAssets{Version: tt.incoming}
			dir := dockerRetryTestRepo(t, dockerversions.Versions{assets.GetDockerRepoVersionsKey(): {Version: tt.current, Revision: tt.revision}})
			var state dockerUpdateState
			err := state.Check(dir, assets)
			if err == nil || !strings.Contains(err.Error(), "refusing to downgrade") {
				t.Fatalf("want downgrade rejection, got %v", err)
			}
		})
	}
}

func TestDockerVersionOlderPrereleaseUpgrades(t *testing.T) {
	for _, tt := range []struct {
		incoming, current string
	}{
		{"1.42.0beta2", "1.42.0beta1"},
		{"1.42.0rc1", "1.42.0beta2"},
		{"1.42.0rc10", "1.42.0rc9"},
		{"1.42.0", "1.42.0rc1"},
		{"1.42.0rc1", "1.42rc1"},
	} {
		t.Run(tt.incoming+"/"+tt.current, func(t *testing.T) {
			if dockerVersionOlder(tt.incoming, "2", tt.current, "1") {
				t.Fatal("rejected prerelease upgrade")
			}
		})
	}
}

func TestDockerUpdateStateRemovedSlot(t *testing.T) {
	assets := &buildassets.BuildAssets{Version: "1.42.1"}
	versions := dockerversions.Versions{"1.42": {Version: "1.42.0"}}
	dir := dockerRetryTestRepo(t, versions)
	var state dockerUpdateState
	if err := state.Check(dir, assets); err != nil {
		t.Fatal(err)
	}
	delete(versions, "1.42")
	writeDockerRetryVersions(t, dir, versions)
	if err := state.Check(dir, assets); err == nil {
		t.Fatal("accepted removal of the selected slot")
	}
}

func TestDockerUpdateStateReadErrorsAndNilAssets(t *testing.T) {
	dir := dockerRetryTestRepo(t, dockerversions.Versions{})
	var state dockerUpdateState
	if err := state.Check(dir, nil); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "src", "microsoft", "versions.json")
	if err := os.WriteFile(path, []byte("{"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := state.Check(dir, &buildassets.BuildAssets{Version: "1.42.1"}); err == nil {
		t.Fatal("accepted malformed versions.json")
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err := state.Check(dir, &buildassets.BuildAssets{Version: "1.42.1"}); err == nil {
		t.Fatal("accepted missing versions.json")
	}
}

func dockerRetryTestRepo(t *testing.T, versions dockerversions.Versions) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "src", "microsoft"), 0o755); err != nil {
		t.Fatal(err)
	}
	writeDockerRetryVersions(t, dir, versions)
	return dir
}

func writeDockerRetryVersions(t *testing.T, dir string, versions dockerversions.Versions) {
	t.Helper()
	if err := stringutil.WriteJSONFile(filepath.Join(dir, "src", "microsoft", "versions.json"), versions); err != nil {
		t.Fatal(err)
	}
}
