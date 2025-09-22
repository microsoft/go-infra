// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package main

// Generate the Azure Pipelines YAML from .gen.yml files for this repo. Normally
// this would be done from eng/, but that dir has a separate, small module for
// now. It's more convenient to launch from here.

//go:generate go run github.com/microsoft/go-infra/cmd/pipelineymlgen -r ../../eng/pipelines
