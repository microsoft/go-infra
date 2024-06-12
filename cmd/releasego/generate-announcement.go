package main

import (
	_ "embed"
	"fmt"
	"strings"
	"text/template"

	"github.com/microsoft/go-infra/subcmd"
)

func init() {
	subcommands = append(subcommands, subcmd.Option{
		Name:    "generate-announcement",
		Summary: "Generate an HTML formatted blog post for Go version releases.",
		Description: `
		The 'generate-announcement' command is used to create an HTML formatted 
		blog post for the Microsoft builds of Go security patch releases. This command requires 
		the release date, the list of released versions, and a release label. It outputs an 
		HTML formatted blog post that includes the release date, the versions released, 
		and a link to the upstream Go announcement.
		`,
		Handle: generateAnnouncement,
	})
}

type ReleaseInfo struct {
	ReleaseDate string
	Versions    []string
	Label       string
	Details     string
}

// VersionDetails generates a formatted string listing the versions.
// Unless there are no edge cases, the output will be most likely generated in first or second format.
// Last clause is added for rare case of more than two versions being released at once.
func (r *ReleaseInfo) VersionDetails() string {
	switch len(r.Versions) {
	case 0:
		return ""
	case 1:
		return fmt.Sprintf("Go %s is released.", r.Versions[0])
	case 2:
		return fmt.Sprintf("Go %s and Go %s are released.", r.Versions[0], r.Versions[1])
	default:
		versionsStr := strings.Join(r.Versions[:len(r.Versions)-1], ", Go ")
		return fmt.Sprintf("Go %s, and Go %s are released.", versionsStr, r.Versions[len(r.Versions)-1])
	}
}

//go:embed templates/announcement.template.md
var announcementTemplatae string

func generateAnnouncement(p subcmd.ParseFunc) error {
	template.New("announcement.template.md")
	return nil
}
