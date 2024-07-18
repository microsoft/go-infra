package infrasort

import (
	"sort"
	"testing"

	"github.com/microsoft/go-infra/goversion"
)

func TestGoVersions_Sort(t *testing.T) {
	tests := []struct {
		name     string
		versions GoVersions
		expected GoVersions
	}{
		{
			name: "basic version sorting",
			versions: GoVersions{
				goversion.New("1.2.3"),
				goversion.New("1.2.1"),
				goversion.New("1.3.0"),
				goversion.New("1.2.3-2"),
				goversion.New("1.2.3-1"),
			},
			expected: GoVersions{
				goversion.New("1.3.0"),
				goversion.New("1.2.3-2"),
				goversion.New("1.2.3"),
				goversion.New("1.2.3-1"),
				goversion.New("1.2.1"),
			},
		},
		{
			name: "version with prerelease and note",
			versions: GoVersions{
				goversion.New("1.2.3-beta"),
				goversion.New("1.2.3-rc1"),
				goversion.New("1.2.3-1-fips"),
				goversion.New("1.2.3"),
				goversion.New("1.2.3-2"),
			},
			expected: GoVersions{
				goversion.New("1.2.3-2"),
				goversion.New("1.2.3"),
				goversion.New("1.2.3-beta"),
				goversion.New("1.2.3-1-fips"),
				goversion.New("1.2.3-rc1"),
			},
		},
		{
			name: "sorting with major and minor versions",
			versions: GoVersions{
				goversion.New("2.0.0"),
				goversion.New("1.10.0"),
				goversion.New("1.2.3"),
				goversion.New("1.2.0"),
				goversion.New("1.3.0"),
			},
			expected: GoVersions{
				goversion.New("2.0.0"),
				goversion.New("1.10.0"),
				goversion.New("1.3.0"),
				goversion.New("1.2.3"),
				goversion.New("1.2.0"),
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Sort the versions
			sort.Sort(tt.versions)
			for i, v := range tt.versions {
				if *v != *tt.expected[i] {
					t.Errorf("expected %v at index %d, got %v", tt.expected[i].Original, i, v.Original)
				}
			}
		})
	}
}
