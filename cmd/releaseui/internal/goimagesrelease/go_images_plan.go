// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package goimagesrelease

import (
	"fmt"
	"sort"
	"strings"

	"github.com/microsoft/go-infra/cmd/releaseui/internal/goimagesworkflow"
	"github.com/microsoft/go-infra/releaseui/contract"
)

func goImagesPlanView(
	input PlanInput,
	source GoImagesSource,
	rollbackSource *GoImagesRollbackSource,
	parameters map[string]string,
	stepCount int,
	restored bool,
) contract.PlanView {
	modeName := string(input.Mode)
	if modeName != "" {
		modeName = strings.ToUpper(modeName[:1]) + modeName[1:]
	}
	view := contract.PlanView{
		Subtitle:    fmt.Sprintf("%s release · pipeline %d · %d steps", modeName, goimagesworkflow.DefinitionID, stepCount),
		IntentBadge: parameters["publishRepoPrefix"],
		Facts: []contract.PlanFact{{
			Label: "Pipeline source", Value: source.Branch, Detail: source.Commit,
		}},
		Request: &contract.RequestPreview{
			Eyebrow: "Azure DevOps request preview · not sent",
			Title:   fmt.Sprintf("Pipeline %d · %s", goimagesworkflow.DefinitionID, goImagesPipelineName),
			Target:  goImagesPipelineOrg + "/" + goImagesPipelineProject,
		},
	}
	if restored {
		view.Subtitle += " · restored from work item"
	}
	for _, name := range sortedMapKeys(parameters) {
		view.Request.Fields = append(view.Request.Fields, contract.RequestField{Name: name, Value: parameters[name]})
	}
	switch input.Mode {
	case goimagesworkflow.ModeNormal:
		view.IntentTitle = "Build current main and publish production images"
		view.ExecutionTitle = "Run production release"
		view.ExecutionWarning = "This builds current main, performs production signing, and publishes production images under public/."
		view.ExecutionConfirmation = "Confirm run to build, sign, and publish current main to public/."
		view.ExecutionButtonLabel = "Run production release"
	case goimagesworkflow.ModeRollback:
		view.IntentTitle = "Republish artifacts from build " + input.SourceBuildID
		view.ExecutionTitle = "Run rollback / republish"
		view.ExecutionWarning = "This republishes artifacts from build " + input.SourceBuildID + " under public/. It does not rebuild those images."
		view.ExecutionConfirmation = "Confirm run to republish artifacts from build " + input.SourceBuildID + " to public/."
		view.ExecutionButtonLabel = "Run rollback"
		if rollbackSource != nil {
			view.Facts = append(view.Facts, contract.PlanFact{
				Label: "Artifact source", Value: fmt.Sprintf("Pipeline %d build %d", goimagesworkflow.DefinitionID, rollbackSource.BuildID),
				Href: rollbackSource.URL,
			})
		}
	case goimagesworkflow.ModeTest:
		view.IntentTitle = "Build current main and publish a dev/ test release"
		view.ExecutionTitle = "Run test release"
		view.ExecutionWarning = "This queues a real build and may use production signing resources, but publication is fixed to dev/ rather than public/."
		view.ExecutionConfirmation = "Confirm run to queue pipeline 1023 with publication locked to dev/."
		view.ExecutionButtonLabel = "Run test release"
	}
	return view
}

func sortedMapKeys(values map[string]string) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}
