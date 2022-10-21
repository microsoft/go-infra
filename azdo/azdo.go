// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package azdo

import (
	"errors"
	"flag"
	"fmt"
	"log"
	"os"

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

// LogCmdSetVariable uses an AzDO logging command to set a variable in the current (build) context.
// https://docs.microsoft.com/en-us/azure/devops/pipelines/scripts/logging-commands?view=azure-devops&tabs=bash#setvariable-initialize-or-modify-the-value-of-a-variable
func LogCmdSetVariable(name, value string) {
	fmt.Printf("##vso[task.setvariable variable=%v]%v\n", name, value)
}

// LogCmdUploadSummary uses an AzDO logging command to upload a summary file. The file is shown on
// the build page in an "Extensions" tab. If it is a Markdown file, it is rendered with a subset of
// Markdown features. The path must be a full path.
// https://docs.microsoft.com/en-us/azure/devops/pipelines/scripts/logging-commands?view=azure-devops&tabs=bash#uploadsummary-add-some-markdown-content-to-the-build-summary
func LogCmdUploadSummary(path string) {
	fmt.Printf("##vso[task.uploadsummary]%v\n", path)
}

// AzDOBuildDetectionDoc describes how AzDO build detection works, listing the env vars used. Use
// this in the command description when using GetEnvBuildID or GetEnvBuildURL.
const AzDOBuildDetectionDoc = "If AzDO env variables SYSTEM_COLLECTIONURI, SYSTEM_TEAMPROJECT, and BUILD_BUILDID are set, includes a link to the build.\n"

func GetBuildURL(collection, project, id string) string {
	if collection == "" || project == "" || id == "" {
		return ""
	}
	return collection + project + "/_build/results?buildId=" + id
}

// GetEnvBuildURL probes the environment to figure out the build URL, if this is running in an AzDO
// pipeline build.
func GetEnvBuildURL() string {
	return GetBuildURL(GetEnvCollectionURI(), GetEnvProject(), GetEnvBuildID())
}

// GetEnvCollectionURI probes the environment to figure out the collection URI, if this is running
// in an AzDO pipeline build.
func GetEnvCollectionURI() string {
	return getEnvNotifyIfEmpty("SYSTEM_COLLECTIONURI")
}

// GetEnvProject probes the environment to figure out the AzDO project, if this is running in an
// AzDO pipeline build.
func GetEnvProject() string {
	return getEnvNotifyIfEmpty("SYSTEM_TEAMPROJECT")
}

// GetEnvBuildID probes the environment to figure out the build ID, if this is running in an AzDO
// pipeline build.
func GetEnvBuildID() string {
	return getEnvNotifyIfEmpty("BUILD_BUILDID")
}

// GetEnvDefinitionName probes the environment to figure out the build definition (pipeline) name,
// if this is running in an AzDO pipeline build.
func GetEnvDefinitionName() string {
	return getEnvNotifyIfEmpty("BUILD_DEFINITIONNAME")
}

// GetEnvAgentJobStatus probes the environment to figure out the status of the current job, if this
// is running in an AzDO pipeline build.
func GetEnvAgentJobStatus() string {
	return getEnvNotifyIfEmpty("AGENT_JOBSTATUS")
}

// getEnvNotifyIfEmpty finds the given environment variable and returns it. If the variable is not
// defined, logs a brief message so the user can diagnose the situation and returns empty string. If
// the variable is empty string, logs a message so the user can diagnose this situation, too.
func getEnvNotifyIfEmpty(key string) string {
	v, ok := os.LookupEnv(key)
	if !ok {
		log.Printf("Env var not defined: %v", key)
	}
	if v == "" {
		log.Printf("Env var defined as empty string: %v", key)
	}
	return v
}
