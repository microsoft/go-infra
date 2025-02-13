// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package main

import (
	"flag"
	"fmt"
	"log"
	"os"

	"github.com/microsoft/go-infra/json2junit"
)

const description = `
json2junit converts a JSON file with Go test output to a JUnit XML file.
`

func main() {
	flag.Usage = func() {
		fmt.Fprintf(flag.CommandLine.Output(), "\nUsage:\n")
		flag.PrintDefaults()
		fmt.Fprintf(flag.CommandLine.Output(), "%s\n\n", description)
	}
	in := flag.String("in", "", "input file")
	out := flag.String("out", "", "output file")
	flag.Parse()

	if *in == "" || *out == "" {
		flag.Usage()
		os.Exit(1)
	}
	if err := json2junit.ConvertFile(*in, *out); err != nil {
		log.Fatalln(err)
	}
}
