package main

import (
	"testing"
)

func TestGenerateAnnouncement(t *testing.T) {

}

func TestGoReleaseVersionLink(t *testing.T) {
	releaseID := "1.22.3"
	expected := "https://go.dev/doc/devel/release#go1.22.3"

	result := createGoReleaseLinkFromVersion(releaseID)
	if result != expected {
		t.Errorf("expected the release link to be %q, but got %q", expected, result)
	}
}

func TestTruncateMSGoVersionTag(t *testing.T) {
	msGoVersion := "1.22.3-1"
	expected := "1.22.3"
	goVersion := truncateMSGoVersionTag(msGoVersion)
	if goVersion != expected {
		t.Errorf("expected the version tag to be truncated to %q, but got %q", expected, msGoVersion)
	}
}
