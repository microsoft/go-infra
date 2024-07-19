package infrasort

import (
	"strconv"

	"github.com/microsoft/go-infra/goversion"
)

// GoVersions implements [sort.Interface] and sorts versions in descending order.
// If Major, Minor, Patch, or Revision of any GoVersion in the slice can't be parsed by
// [strconv.Atoi], the result of using this type is undefined.
type GoVersions []*goversion.GoVersion

func (versions GoVersions) Len() int      { return len(versions) }
func (versions GoVersions) Swap(i, j int) { versions[i], versions[j] = versions[j], versions[i] }
func (versions GoVersions) Less(i, j int) bool {
	less := func(a, b string) bool {
		intA, err := strconv.Atoi(a)
		if err != nil {
			return false
		}

		intB, err := strconv.Atoi(b)
		if err != nil {
			return false
		}
		return intA > intB
	}

	current, next := versions[i], versions[j]

	if current.Major != next.Major {
		return less(current.Major, next.Major)
	}
	if current.Minor != next.Minor {
		return less(current.Minor, next.Minor)
	}
	if current.Patch != next.Patch {
		return less(current.Patch, next.Patch)
	}
	if current.Revision != next.Revision {
		return less(current.Revision, next.Revision)
	}
	if current.Prerelease != next.Prerelease {
		return current.Prerelease < next.Prerelease
	}
	if current.Note != next.Note {
		return current.Note < next.Note
	}
	return false
}
