// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package main

import (
	"errors"
	"flag"
	"fmt"
	"os"

	"github.com/microsoft/go-infra/internal/pipelineymlgen"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintf(os.Stderr, "%v\n", err)
		if errors.Is(err, pipelineymlgen.ErrFileDiffers) {
			os.Exit(2)
		}
		os.Exit(1)
	}
}

func run() error {
	flags := pipelineymlgen.BindCmdFlags()

	flag.Usage = func() {
		fmt.Fprintf(flag.CommandLine.Output(), "Usage: %s [flags] <file|directory>...\n\n", os.Args[0])
		fmt.Fprintln(flag.CommandLine.Output(), "Generate Azure Pipelines YML by processing ${ ... } template expressions.")
		fmt.Fprintln(flag.CommandLine.Output(), "Takes *.gen.yml files and directories containing them and generates corresponding *.yml files.")
		fmt.Fprintln(flag.CommandLine.Output())
		fmt.Fprintln(flag.CommandLine.Output(), "Flags:")
		flag.PrintDefaults()
	}

	flag.Parse()

	return pipelineymlgen.Run(flags, flag.Args()...)
}
