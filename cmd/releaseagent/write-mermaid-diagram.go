// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package main

import (
	"flag"
	"fmt"

	"github.com/microsoft/go-infra/cmd/releaseagent/internal/coordinator"
	"github.com/microsoft/go-infra/cmd/releaseagent/internal/releasesteps"
	"github.com/microsoft/go-infra/subcmd"
)

func init() {
	subcommands = append(subcommands, subcmd.Option{
		Name:        "write-mermaid-diagram",
		Summary:     "Write the text for a Mermaid diagram of a release plan with the given inputs",
		Description: "",
		Handle:      handleGetPlan,
	})
}

func handleGetPlan(p subcmd.ParseFunc) error {
	input := BindInputFlags()

	open := flag.Bool(
		"url", false,
		"Print a URL to view the Mermaid diagram in a browser instead of printing the diagram source code")
	edit := flag.Bool(
		"edit", false,
		"Make the URL, if printed, go to 'edit' mode rather than the full-screen diagram viewer")

	if err := p(); err != nil {
		return err
	}

	steps, _, err := releasesteps.CreateStepGraph(input, nil, nil, nil)
	if err != nil {
		return err
	}

	chartSource := coordinator.CreateMermaidStepFlowchart(steps)

	if !*open {
		fmt.Printf("Mermaid diagram source code:\n%s\n", chartSource)
		return nil
	}

	url, err := coordinator.MermaidLiveChartURL(chartSource, *edit)
	if err != nil {
		return err
	}

	fmt.Printf("Mermaid live viewer URL:\n%s\n", url)
	return nil
}
