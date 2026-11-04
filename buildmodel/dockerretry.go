// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package buildmodel

import (
	"bytes"
	"encoding/json"
	"fmt"
	"go/version"
	"strconv"

	"github.com/microsoft/go-infra/buildmodel/buildassets"
	"github.com/microsoft/go-infra/buildmodel/dockerversions"
	"github.com/microsoft/go-infra/goversion"
)

// dockerUpdateState belongs to one SubmitUpdatePR call, not one retry attempt.
// It prevents a retry from overwriting another build's update to the same slot.
type dockerUpdateState struct {
	key           string
	before, after []byte
}

// prepare updates the in-memory versions once, validating retry state before the
// caller writes any files. A nil state is an ordinary, unguarded local update.
func (s *dockerUpdateState) prepare(versions dockerversions.Versions, assets *buildassets.BuildAssets) error {
	if assets == nil {
		return nil
	}
	if s == nil {
		return UpdateVersions(assets, versions)
	}
	key := assets.GetDockerRepoVersionsKey()
	before, err := dockerUpdateSnapshot(versions[key])
	if err != nil {
		return err
	}
	if s.key != "" {
		if key != s.key || (!bytes.Equal(before, s.before) && !bytes.Equal(before, s.after)) {
			return fmt.Errorf("stale Docker update: concurrent update changed versions.json slot %q", key)
		}
	}

	// UpdateVersions normalizes asset architectures. Keep the retry input unchanged.
	data, err := json.Marshal(assets)
	if err != nil {
		return err
	}
	var update buildassets.BuildAssets
	if err := json.Unmarshal(data, &update); err != nil {
		return err
	}
	previous := versions[key]
	var previousVersion, previousRevision string
	if previous != nil {
		previousVersion, previousRevision = previous.Version, previous.Revision
	}
	if err := UpdateVersions(&update, versions); err != nil {
		return err
	}
	intended := versions[key]
	if dockerVersionOlder(intended.Version, intended.Revision, previousVersion, previousRevision) {
		return fmt.Errorf("stale Docker update: refusing to downgrade versions.json slot %q from %s-%s to %s-%s",
			key, previousVersion, previousRevision, intended.Version, intended.Revision)
	}
	after, err := dockerUpdateSnapshot(intended)
	if err != nil {
		return err
	}
	if s.key == "" {
		s.key, s.before, s.after = key, before, after
	}
	return nil
}

func dockerUpdateSnapshot(v *dockerversions.MajorMinorVersion) ([]byte, error) {
	if v == nil {
		return json.Marshal(nil)
	}
	// Preferences and other metadata may change when another branch updates.
	return json.Marshal(struct {
		Version  string
		Revision string
		Arches   map[string]*dockerversions.Arch
	}{v.Version, v.Revision, v.Arches})
}

func dockerVersionOlder(incoming, incomingRevision, current, currentRevision string) bool {
	normalize := func(v string) string {
		parsed := goversion.New(v)
		// UpdateVersions includes a zero patch before beta/rc, unlike Go tags.
		if parsed.Patch == "0" && parsed.Prerelease != "" {
			return "go" + parsed.MajorMinor() + parsed.Prerelease
		}
		return "go" + v
	}
	incoming, current = normalize(incoming), normalize(current)
	if !version.IsValid(incoming) || !version.IsValid(current) {
		return false
	}
	if cmp := version.Compare(incoming, current); cmp != 0 {
		return cmp < 0
	}
	if incomingRevision == "" {
		incomingRevision = "1"
	}
	if currentRevision == "" {
		currentRevision = "1"
	}
	in, inErr := strconv.ParseUint(incomingRevision, 10, 64)
	cur, curErr := strconv.ParseUint(currentRevision, 10, 64)
	return inErr == nil && curErr == nil && in < cur
}
