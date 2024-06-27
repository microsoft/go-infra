// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package main

import (
	"encoding/json"
	"fmt"
	"log"
	"os"

	"github.com/microsoft/go-infra/patch"
	"github.com/microsoft/go-infra/subcmd"
)

func init() {
	subcommands = append(subcommands, subcmd.Option{
		Name:        "init",
		Summary:     "Init a default config file in the current working directory.",
		Description: "",
		Handle:      handleInit,
	})
}

func handleInit(p subcmd.ParseFunc) error {
	if err := p(); err != nil {
		return err
	}

	// The default content intended to be replaced with actual values.
	c := patch.Config{
		MinimumToolVersion: version,
		SubmoduleDir:       "path/to/submodule",
		PatchesDir:         "patches",
		StatusFileDir:      "artifacts/go-patch",
	}
	data, err := json.MarshalIndent(&c, "", "  ")
	if err != nil {
		return err
	}

	// Don't overwrite existing file.
	f, err := os.OpenFile(patch.ConfigFileName, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o666)
	if err != nil {
		return fmt.Errorf("unable to create new config file: %v", err)
	}
	defer f.Close()

	if _, err := f.Write(data); err != nil {
		return err
	}

	log.Printf("Wrote default config file: %v\n", patch.ConfigFileName)
	log.Printf("Now, open the file and configure the paths to match your repository:\n%v\n", string(data))
	log.Println("If you would like to add comments to the file explaining its purpose, use properties with names beginning with '__'. " +
		"The config file format will never include any valid properties with this prefix. " +
		"Line comments don't work because the deserializer expects JSON only.")
	log.Println("See https://github.com/microsoft/go-infra/blob/main/patch/config.go for more info about the config file format.")
	return nil
}
