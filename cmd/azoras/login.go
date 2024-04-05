// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package main

import (
	"flag"
	"os"
	"os/exec"

	"github.com/microsoft/go-infra/subcmd"
)

func init() {
	subcommands = append(subcommands, subcmd.Option{
		Name:           "login",
		Summary:        "Log in to the Azure Container Registry using the Azure CLI, which needs to be installed.",
		Description:    "",
		Handle:         handleLogin,
		TakeArgsReason: "The name of the Azure Container Registry to log in to.",
	})
}

func handleLogin(p subcmd.ParseFunc) error {
	if err := p(); err != nil {
		return err
	}
	if _, err := exec.LookPath("oras"); err != nil {
		return err
	}
	acr := flag.Arg(0)
	cmdAz := exec.Command("az", "acr", "login", "--name", acr, "--expose-token", "--output", "tsv", "--query", "accessToken")
	cmdOras := exec.Command("oras", "login", "--password-stdin", acr)
	var err error
	cmdOras.Stdin, err = cmdAz.StdoutPipe()
	if err != nil {
		return err
	}
	cmdOras.Stdout = os.Stdout
	if err := cmdOras.Start(); err != nil {
		return err
	}
	if err := cmdAz.Run(); err != nil {
		return err
	}
	return cmdOras.Wait()
}
