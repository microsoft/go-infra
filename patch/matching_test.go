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
	tests, err := filepath.Glob("testdata/TestApply*")
	if err != nil {
		t.Fatal(err)
	}

	for _, testDir := range tests {
		name := filepath.Base(testDir)
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			m := tempMoremathRepo(t, filepath.Join("testdata", name, "before"))
			if err := WalkPatches(filepath.Join("testdata", name, "after"), func(path string) error {
				p, err := ReadFile(path)
				if err != nil {
					return err
				}
				matchPath, err := m.CheckedApply(path, p)
				if err != nil {
					return err
				}
				// Use an indicator in the patch file path to determine whether we expect a match or
				// not. This isn't precise: we don't keep track of which patch file should be
				// matched. This would either require more test-specific code in CheckedApply or more
				// intricate commit hash tracking, and it's not worthwhile for these scenario tests.
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

// TestMatchCheckRepo_ApplyWithThreeWayConfig verifies that CheckedApply produces the same results
// even when the user has am.threeWay=true configured. The Am function should override this, so test
// outcomes should be identical to the base TestMatchCheckRepo_Apply. This guards against a
// regression where am.threeWay leaks through and causes extract to behave differently than CI.
// See https://github.com/microsoft/go/issues/1233.
func TestMatchCheckRepo_ApplyWithThreeWayConfig(t *testing.T) {
	tests, err := filepath.Glob("testdata/TestApply*")
	if err != nil {
		t.Fatal(err)
	}

	for _, testDir := range tests {
		name := filepath.Base(testDir)
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			m := tempMoremathRepo(t, filepath.Join("testdata", name, "before"))

			// Simulate a user who has am.threeWay=true by setting it in the temp clone's config.
			if err := gitcmd.Run(m.gitDir, "config", "am.threeWay", "true"); err != nil {
				t.Fatalf("failed to set am.threeWay=true in temp repo: %v", err)
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
