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
	"github.com/microsoft/go-infra/buildmodel/dockermanifest"
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
			var state dockerUpdateState
			if err := state.prepare(versions, assets); err != nil {
				t.Fatal(err)
			}
			tt.change(versions[key])
			for range 2 {
				if err := state.prepare(versions, assets); err == nil || !strings.Contains(err.Error(), "stale Docker update") {
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
			initialVersions := func() dockerversions.Versions {
				return dockerversions.Versions{
					"1.42": {Version: "1.42.0", Arches: map[string]*dockerversions.Arch{"amd64": {URL: "old"}}},
					"1.41": {Version: "1.41.0"},
				}
			}
			versions := initialVersions()
			var state dockerUpdateState
			if err := state.prepare(versions, assets); err != nil {
				t.Fatal(err)
			}
			if !partialPush {
				versions = initialVersions()
			}
			versions["1.41"].Version = "1.41.2"
			versions["1.42"].PreferredMajor = true
			versions["1.42"].PreferredMinor = true
			versions["1.42"].Variants = []string{"new-variant"}
			if err := state.prepare(versions, assets); err != nil {
				t.Fatal(err)
			}
			if versions["1.42"].Version != "1.42.1" || versions["1.42"].Arches["amd64"].URL != "incoming" {
				t.Fatal("retry did not prepare incoming version and artifacts")
			}
			if versions["1.41"].Version != "1.41.2" || !versions["1.42"].PreferredMajor ||
				!versions["1.42"].PreferredMinor || versions["1.42"].Variants[0] != "new-variant" {

				t.Fatal("retry lost unrelated changes")
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
	versions := dockerversions.Versions{"1.42": {Version: "1.42.0"}}
	before, err := json.Marshal(assets)
	if err != nil {
		t.Fatal(err)
	}
	var state dockerUpdateState
	if err := state.prepare(versions, assets); err != nil {
		t.Fatal(err)
	}
	after, err := json.Marshal(assets)
	if err != nil {
		t.Fatal(err)
	}
	if string(before) != string(after) {
		t.Fatalf("preparation mutated assets: before %s, after %s", before, after)
	}
	if versions["1.42"].Version != "1.42.1" || versions["1.42"].Arches["arm32v7"].Env.GOARM != "7" {
		t.Fatal("preparation did not update versions and normalize ARM artifacts")
	}
	if err := state.prepare(versions, assets); err != nil {
		t.Fatalf("guard did not predict normalized ARM artifacts: %v", err)
	}
}

func TestDockerUpdateStateNewSlot(t *testing.T) {
	assets := &buildassets.BuildAssets{Version: "1.42.1"}
	versions := dockerversions.Versions{"1.41": {Version: "1.41.0"}}
	var state dockerUpdateState
	if err := state.prepare(versions, assets); err != nil {
		t.Fatal(err)
	}
	versions = dockerversions.Versions{"1.41": {Version: "1.41.0"}}
	if err := state.prepare(versions, assets); err != nil {
		t.Fatal(err)
	}
	if err := state.prepare(versions, assets); err != nil {
		t.Fatal(err)
	}
	versions["1.42"].Version = "1.42.2"
	if err := state.prepare(versions, assets); err == nil {
		t.Fatal("accepted concurrent creation of newer slot")
	}
}

func TestDockerUpdateStateUnmatchedSlot(t *testing.T) {
	versions := dockerversions.Versions{"1.40": {Version: "1.40.0"}}
	assets := &buildassets.BuildAssets{Version: "1.42.1"}
	var state dockerUpdateState
	if err := state.prepare(versions, assets); !errors.Is(err, ErrNoMajorMinorUpgradeMatch) {
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
			versions := dockerversions.Versions{assets.GetDockerRepoVersionsKey(): {Version: tt.current, Revision: tt.revision}}
			var state dockerUpdateState
			err := state.prepare(versions, assets)
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
	var state dockerUpdateState
	if err := state.prepare(versions, assets); err != nil {
		t.Fatal(err)
	}
	delete(versions, "1.42")
	if err := state.prepare(versions, assets); err == nil {
		t.Fatal("accepted removal of the selected slot")
	}
}

func TestDockerUpdateStateNilAssets(t *testing.T) {
	var state dockerUpdateState
	if err := state.prepare(nil, nil); err != nil {
		t.Fatal(err)
	}
}

func TestUpdateGoImagesRepoReadErrors(t *testing.T) {
	for _, path := range []string{"src/microsoft/versions.json", "manifest.json"} {
		t.Run(path, func(t *testing.T) {
			dir := dockerRetryTestRepo(t, dockerversions.Versions{"1.42": {Version: "1.42.0"}})
			path := filepath.Join(dir, path)
			var state dockerUpdateState
			if err := os.WriteFile(path, []byte("{"), 0o600); err != nil {
				t.Fatal(err)
			}
			if err := updateGoImagesRepo(dir, &buildassets.BuildAssets{Version: "1.42.1"}, &state); err == nil {
				t.Fatalf("accepted malformed %s", path)
			}
			if err := os.Remove(path); err != nil {
				t.Fatal(err)
			}
			if err := updateGoImagesRepo(dir, &buildassets.BuildAssets{Version: "1.42.1"}, &state); err == nil {
				t.Fatalf("accepted missing %s", path)
			}
		})
	}
}

func TestUpdateGoImagesRepoPreparedUpdate(t *testing.T) {
	versions := dockerversions.Versions{"1.42": {Version: "1.42.0", Variants: []string{"bookworm"}}}
	dir := dockerRetryTestRepo(t, versions)
	assets := &buildassets.BuildAssets{
		Version: "1.42.1",
		Arches: []*dockerversions.Arch{{
			Env: &dockerversions.ArchEnv{GOOS: "linux", GOARCH: "arm", GOARM: "6"},
			URL: "incoming", SHA256: "incoming",
		}},
	}
	var state dockerUpdateState
	if err := updateGoImagesRepo(dir, assets, &state); err != nil {
		t.Fatal(err)
	}
	versionsPath := filepath.Join(dir, "src", "microsoft", "versions.json")
	if err := stringutil.ReadJSONFile(versionsPath, &versions); err != nil {
		t.Fatal(err)
	}
	arch := versions["1.42"].Arches["arm32v7"]
	if versions["1.42"].Version != "1.42.1" || arch == nil || arch.Env.GOARM != "7" ||
		arch.URL != "incoming" || arch.SHA256 != "incoming" {

		t.Fatalf("incorrect prepared versions: %+v, arch %+v", versions["1.42"], arch)
	}
	if assets.Arches[0].Env.GOARM != "6" || assets.Arches[0].Supported {
		t.Fatal("repository update mutated incoming assets")
	}
	manifestPath := filepath.Join(dir, "manifest.json")
	var manifest dockermanifest.Manifest
	if err := stringutil.ReadJSONFile(manifestPath, &manifest); err != nil {
		t.Fatal(err)
	}
	if len(manifest.Repos) != 1 || len(manifest.Repos[0].Images) != 1 {
		t.Fatalf("incorrect prepared manifest: %+v", manifest)
	}
	image := manifest.Repos[0].Images[0]
	if _, ok := image.SharedTags["1.42.1-bookworm"]; !ok || len(image.Platforms) != 1 {
		t.Fatalf("manifest did not use prepared versions: %+v", image)
	}
	if image.Platforms[0].Architecture != "arm" || image.Platforms[0].Variant != "v7" {
		t.Fatalf("manifest did not use normalized ARM artifact: %+v", image.Platforms[0])
	}

	versions["1.42"].Version = "1.42.2"
	writeDockerRetryVersions(t, dir, versions)
	before := map[string][]byte{}
	for _, path := range []string{versionsPath, manifestPath} {
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		before[path] = data
	}
	for _, tt := range []struct {
		name  string
		state *dockerUpdateState
	}{
		{"concurrent update", &state},
		{"initial downgrade", &dockerUpdateState{}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			if err := updateGoImagesRepo(dir, assets, tt.state); err == nil || !strings.Contains(err.Error(), "stale Docker update") {
				t.Fatalf("want stale update rejection, got %v", err)
			}
			for path, want := range before {
				got, err := os.ReadFile(path)
				if err != nil {
					t.Fatal(err)
				}
				if string(got) != string(want) {
					t.Fatalf("stale update changed %s", path)
				}
			}
		})
	}
}

func TestUpdateGoImagesRepoNilAssets(t *testing.T) {
	dir := dockerRetryTestRepo(t, dockerversions.Versions{
		"1.42": {Version: "1.42.0", Variants: []string{"bookworm"}},
	})
	versionsPath := filepath.Join(dir, "src", "microsoft", "versions.json")
	before, err := os.ReadFile(versionsPath)
	if err != nil {
		t.Fatal(err)
	}
	var state dockerUpdateState
	if err := updateGoImagesRepo(dir, nil, &state); err != nil {
		t.Fatal(err)
	}
	after, err := os.ReadFile(versionsPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(before) != string(after) || state.key != "" {
		t.Fatal("manifest-only update changed versions or retry state")
	}
	var manifest dockermanifest.Manifest
	if err := stringutil.ReadJSONFile(filepath.Join(dir, "manifest.json"), &manifest); err != nil {
		t.Fatal(err)
	}
	if len(manifest.Repos) != 1 || len(manifest.Repos[0].Images) != 1 {
		t.Fatalf("manifest-only update did not generate images: %+v", manifest)
	}
	if _, ok := manifest.Repos[0].Images[0].SharedTags["1.42.0-bookworm"]; !ok {
		t.Fatal("manifest-only update did not use existing versions")
	}
}

func dockerRetryTestRepo(t *testing.T, versions dockerversions.Versions) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "src", "microsoft"), 0o755); err != nil {
		t.Fatal(err)
	}
	writeDockerRetryVersions(t, dir, versions)
	if err := os.WriteFile(filepath.Join(dir, "manifest.json"), []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	return dir
}

func writeDockerRetryVersions(t *testing.T, dir string, versions dockerversions.Versions) {
	t.Helper()
	if err := stringutil.WriteJSONFile(filepath.Join(dir, "src", "microsoft", "versions.json"), versions); err != nil {
		t.Fatal(err)
	}
}
