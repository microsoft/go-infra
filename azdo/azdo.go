// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package azdo

import (
	"errors"
	"flag"
	"fmt"

	"github.com/microsoft/azure-devops-go-api/azuredevops"
	"github.com/microsoft/azure-devops-go-api/azuredevops/build"
)

// ClientFlags is a set of command-line flags always used in go-infra to access the AzDO APIs.
type ClientFlags struct {
	// Org is the base URL of the organization, such as 'https://dev.azure.com/dnceng/'
	Org *string
	// Proj is the project, such as 'public' or 'internal'.
	Proj *string
	// PAT is the Personal Access Token to use.
	PAT *string
}

// BindClientFlags creates a ClientFlags struct where each field is set up as a flag.
func BindClientFlags() *ClientFlags {
	return &ClientFlags{
		Org:  flag.String("org", "", "[Required] The AzDO organization URL."),
		Proj: flag.String("proj", "", "[Required] The AzDO project URL."),
		PAT:  flag.String("azdopat", "", "[Required] The Azure DevOps PAT to use."),
	}
}

// EnsureAssigned can be called after "flag.Parse()" to ensure all required flags were specified.
func (c *ClientFlags) EnsureAssigned() error {
	if *c.Org == "" {
		return errors.New("no AzDO org specified")
	}
	if *c.Proj == "" {
		return errors.New("no AzDO project URL specified")
	}
	if *c.PAT == "" {
		return errors.New("no AzDO PAT specified")
	}
	return nil
}

// NewConnection creates an AzDO connection based on the given flags.
func (c *ClientFlags) NewConnection() *azuredevops.Connection {
	return azuredevops.NewPatConnection(*c.Org, *c.PAT)
}

// GetBuildWebURL finds the web/UI URL (not API endpoint URL) in the given AzDO Build, if it exists.
func GetBuildWebURL(b *build.Build) (string, bool) {
	links, ok := b.Links.(map[string]interface{})
	if !ok {
		return "", false
	}
	web, ok := links["web"].(map[string]interface{})
	if !ok {
		return "", false
	}
	href, ok := web["href"]
	if !ok {
		return "", false
	}
	s, ok := href.(string)
	if !ok {
		return "", false
	}
	return s, true
}

// SetPipelineVariable uses an AzDO logging command to set a variable in the pipeline.
// https://github.com/Microsoft/azure-pipelines-tasks/blob/master/docs/authoring/commands.md
func SetPipelineVariable(name, value string) {
	fmt.Printf("##vso[task.setvariable variable=%v]%v\n", name, value)
}
