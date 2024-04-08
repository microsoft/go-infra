// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package main

import (
	"bytes"
	"fmt"
	"os/exec"

	"github.com/microsoft/go-infra/subcmd"
)

func init() {
	subcommands = append(subcommands, subcmd.Option{
		Name:        "version",
		Summary:     "Print the version of the azoras tool and the ORAS CLI.",
		Description: "",
		Handle:      handleVersion,
	})
}

func handleVersion(p subcmd.ParseFunc) error {
	if err := p(); err != nil {
		return err
	}
	fmt.Printf("azoras version: %s\n", version)
	if _, err := exec.LookPath("oras"); err != nil {
		fmt.Printf("oras not found\n")
		return nil
	}
	orasVersion, err := exec.Command("oras", "version").CombinedOutput()
	if err != nil {
		fmt.Printf("oras version: error: %v\n", err)
		return nil
	}
	orasVersion = bytes.ReplaceAll(orasVersion, []byte("\n"), []byte("\n\t"))
	fmt.Printf("ORAS CLI version:\n\t%s\n", orasVersion)
	return nil
}
