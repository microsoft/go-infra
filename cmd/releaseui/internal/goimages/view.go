// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package goimages

import (
	"fmt"
	"sort"

	"github.com/microsoft/go-infra/releaseui/contract"
)

func goImagesPlan(
	input PlanInput,
	source Source,
	rollbackSource *RollbackSource,
	parameters map[string]string,
	stepCount int,
) *contract.Plan {
	plan := &contract.Plan{
		Subtitle: fmt.Sprintf("%s · pipeline %d · %d steps", modeName(input.Mode), DefinitionID, stepCount),
		Facts: []contract.PlanFact{{
			Label: "Pipeline source", Value: source.Branch, Detail: source.Commit,
		}},
	}
	plan.Facts = append(plan.Facts,
		contract.PlanFact{Label: "Pipeline", Value: fmt.Sprintf("%d · %s", DefinitionID, goImagesPipelineName)},
		contract.PlanFact{Label: "Target", Value: goImagesPipelineOrg + "/" + goImagesPipelineProject},
	)
	for _, name := range sortedMapKeys(parameters) {
		plan.Facts = append(plan.Facts, contract.PlanFact{Label: name, Value: parameters[name]})
	}
	switch input.Mode {
	case ModeNormal:
		plan.ExecutionButtonLabel = "Run production release"
	case ModeRollback:
		plan.ExecutionButtonLabel = "Run rollback"
		if rollbackSource != nil {
			plan.Facts = append(plan.Facts, contract.PlanFact{
				Label: "Artifact source", Value: fmt.Sprintf("Pipeline %d build %d", DefinitionID, rollbackSource.BuildID),
				Detail: fmt.Sprintf(`<a href="%s" target="_blank" rel="noreferrer">Open build</a>`, rollbackSource.URL),
			})
		}
	case ModeTest:
		plan.ExecutionButtonLabel = "Run test release"
	}
	return plan
}

func sortedMapKeys(values map[string]string) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}
