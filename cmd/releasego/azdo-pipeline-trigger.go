package main

import "github.com/microsoft/go-infra/subcmd"

func init() {
	subcommands = append(subcommands, subcmd.Option{
		Name:    "azdo-pipeline-trigger",
		Summary: "Trigger the Azure DevOps pipeline for Azure Linux.",
		Description: `
This command triggers the Azure DevOps pipeline for Azure Linux.
`,
		Handle: updateAzureLinux,
	})
}
