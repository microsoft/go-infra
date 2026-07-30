// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package goimagesrelease

import (
	"errors"
	"fmt"

	"go.yaml.in/yaml/v4"
)

const (
	pipelineInfoValue           = "🔵  go-docker-rolling-internal-pipeline.yml  🔵 🔵"
	unofficialPipelineInfoValue = "🔵  go-docker-rolling-internal-pipeline-unofficial.yml  🔵 🔵"
)

type expectedParameter struct {
	defaultValue string
	values       []string
}

var expectedPipelineParameters = map[string]expectedParameter{
	"_info": {
		defaultValue: pipelineInfoValue,
		values:       []string{pipelineInfoValue},
	},
	"sourceBuildPipelineRunId": {defaultValue: "$(Build.BuildId)"},
	"publishRepoPrefix":        {defaultValue: "public/"},
}

var expectedUnofficialPipelineParameters = map[string]expectedParameter{
	"_info": {
		defaultValue: unofficialPipelineInfoValue,
		values:       []string{unofficialPipelineInfoValue},
	},
	"sourceBuildPipelineRunId": {defaultValue: "$(Build.BuildId)"},
	"publishRepoPrefix":        {defaultValue: "dev/"},
}

// ValidatePipelineParameterContract verifies the direct official go-images pipeline's complete
// runtime parameter surface and defaults. Any drift must be reviewed before the UI can use Azure.
func ValidatePipelineParameterContract(data []byte) error {
	return validatePipelineParameterContract(data, expectedPipelineParameters)
}

// ValidateUnofficialPipelineParameterContract verifies the complete nonproduction parameter
// surface used for a real demo. In particular, publishing must default to dev/.
func ValidateUnofficialPipelineParameterContract(data []byte) error {
	return validatePipelineParameterContract(data, expectedUnofficialPipelineParameters)
}

func validatePipelineParameterContract(data []byte, expected map[string]expectedParameter) error {
	if len(data) == 0 {
		return errors.New("go-images pipeline YAML is empty")
	}
	var pipeline struct {
		Parameters []struct {
			Name    string `yaml:"name"`
			Type    string `yaml:"type"`
			Default any    `yaml:"default"`
			Values  []any  `yaml:"values"`
		} `yaml:"parameters"`
	}
	if err := yaml.Unmarshal(data, &pipeline); err != nil {
		return fmt.Errorf("parse go-images pipeline YAML: %w", err)
	}
	if len(pipeline.Parameters) != len(expected) {
		return fmt.Errorf(
			"go-images pipeline declares %d runtime parameters, expected %d",
			len(pipeline.Parameters),
			len(expected),
		)
	}
	seen := make(map[string]struct{}, len(pipeline.Parameters))
	for _, parameter := range pipeline.Parameters {
		want, ok := expected[parameter.Name]
		if !ok {
			return fmt.Errorf("go-images pipeline declares unexpected runtime parameter %q", parameter.Name)
		}
		if _, ok := seen[parameter.Name]; ok {
			return fmt.Errorf("go-images pipeline declares runtime parameter %q more than once", parameter.Name)
		}
		seen[parameter.Name] = struct{}{}
		if parameter.Type != "string" {
			return fmt.Errorf("go-images pipeline parameter %q has type %q, expected string", parameter.Name, parameter.Type)
		}
		gotDefault, ok := parameter.Default.(string)
		if !ok || gotDefault != want.defaultValue {
			return fmt.Errorf(
				"go-images pipeline parameter %q has default %#v, expected %q",
				parameter.Name,
				parameter.Default,
				want.defaultValue,
			)
		}
		if len(parameter.Values) != len(want.values) {
			return fmt.Errorf(
				"go-images pipeline parameter %q declares %d allowed values, expected %d",
				parameter.Name,
				len(parameter.Values),
				len(want.values),
			)
		}
		for index, wantValue := range want.values {
			gotValue, ok := parameter.Values[index].(string)
			if !ok || gotValue != wantValue {
				return fmt.Errorf(
					"go-images pipeline parameter %q allowed value %d is %#v, expected %q",
					parameter.Name,
					index,
					parameter.Values[index],
					wantValue,
				)
			}
		}
	}
	return nil
}
