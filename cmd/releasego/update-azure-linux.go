// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package main

import "github.com/microsoft/go-infra/subcmd"

func init() {
	subcommands = append(subcommands, subcmd.Option{
		Name:        "update-azure-linux",
		Summary:     "Update the Azure Linux Microsoft Go version after release.",
		Description: "",
		Handle:      updateAzureLinux,
	})
}

func updateAzureLinux(p subcmd.ParseFunc) error {
	return nil
}
