// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package patch

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/microsoft/go-infra/gitcmd"
)

func TestMatchCheckRepo_Apply(t *testing.T) {
	testMatchCheckRepo(t, nil)
}

// TestMatchCheckRepo_ApplyWithThreeWayConfig verifies that CheckedApply produces the same results
// even when the user has am.threeWay=true configured. The Am function should override this, so test
// outcomes should be identical to the base TestMatchCheckRepo_Apply. This guards against a
// regression where am.threeWay leaks through and causes extract to behave differently than CI.
// See https://github.com/microsoft/go/issues/1233.
func TestMatchCheckRepo_ApplyWithThreeWayConfig(t *testing.T) {
	testMatchCheckRepo(t, func(t *testing.T, m *MatchCheckRepo) {
		// Simulate a user who has am.threeWay=true by setting it in the temp clone's config.
		if err := gitcmd.Run(m.gitDir, "config", "am.threeWay", "true"); err != nil {
			t.Fatalf("failed to set am.threeWay=true in temp repo: %v", err)
		}
	})
}

// testMatchCheckRepo runs the standard set of patch apply scenarios. If configureRepo is non-nil,
// it is called after creating the MatchCheckRepo to allow test-specific configuration.
func testMatchCheckRepo(t *testing.T, configureRepo func(*testing.T, *MatchCheckRepo)) {
	t.Helper()
	tests, err := filepath.Glob("testdata/TestApply*")
	if err != nil {
		t.Fatal(err)
	}

	for _, testDir := range tests {
		name := filepath.Base(testDir)
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			m := tempMoremathRepo(t, filepath.Join("testdata", name, "before"))

			if configureRepo != nil {
				configureRepo(t, m)
			}

			if err := WalkPatches(filepath.Join("testdata", name, "after"), func(path string) error {
				p, err := ReadFile(path)
				if err != nil {
					return err
				}
				matchPath, err := m.CheckedApply(path, p)
				if err != nil {
					return err
				}
				// Use an indicator in the patch file path to determine whether we expect a match.
				wantMatch := strings.HasSuffix(path, "_matching.patch")
				match := matchPath != ""
				if wantMatch != match {
					t.Errorf("applying patch %#q want match = %v, but match = %v", path, wantMatch, match)
				}
				return nil
			}); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func tempMoremathRepo(t *testing.T, patchDir string) *MatchCheckRepo {
	m, err := NewMatchCheckRepo(filepath.Join("testdata", "moremath.pack"), "v1.0.2", patchDir)
	if err != nil {
		t.Fatal(err)
	}
	return m
}
