// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package main

import (
	"os"

	"github.com/microsoft/go-infra/subcmd"
)

func init() {
	subcommands = append(subcommands, subcmd.Option{
		Name:    "clean",
		Summary: "Removes all data in the cache directory, even if this tool could not have created it in its current state.",
		Handle:  clean,
	})
}

func clean(p subcmd.ParseFunc) error {
	if err := p(); err != nil {
		return err
	}
	mingwCacheDir, err := cacheDir()
	if err != nil {
		return err
	}
	if err := os.RemoveAll(mingwCacheDir); err != nil {
		return err
	}
	return nil
}
