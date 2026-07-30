// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package goimagesrelease

import (
	"strings"
	"testing"
)

func TestValidatePipelineParameterContract(t *testing.T) {
	valid := "parameters:\n" +
		"  - name: _info\n" +
		"    type: string\n" +
		"    values:\n" +
		"      - '🔵  go-docker-rolling-internal-pipeline.yml  🔵 🔵'\n" +
		"    default: '🔵  go-docker-rolling-internal-pipeline.yml  🔵 🔵'\n" +
		"  - name: sourceBuildPipelineRunId\n" +
		"    type: string\n" +
		"    default: '$(Build.BuildId)'\n" +
		"  - name: publishRepoPrefix\n" +
		"    type: string\n" +
		"    default: public/\n"
	for _, test := range []struct {
		name    string
		yaml    string
		wantErr string
	}{
		{name: "valid", yaml: valid},
		{name: "empty", wantErr: "empty"},
		{
			name:    "missing parameter",
			yaml:    strings.Replace(valid, "  - name: publishRepoPrefix\n    type: string\n    default: public/\n", "", 1),
			wantErr: "declares 2 runtime parameters",
		},
		{
			name:    "unexpected parameter",
			yaml:    strings.Replace(valid, "publishRepoPrefix", "unexpected", 1),
			wantErr: `unexpected runtime parameter "unexpected"`,
		},
		{
			name:    "wrong type",
			yaml:    strings.Replace(valid, "type: string", "type: boolean", 1),
			wantErr: "expected string",
		},
		{
			name:    "wrong default",
			yaml:    strings.Replace(valid, "default: public/", "default: dev/", 1),
			wantErr: `expected "public/"`,
		},
		{
			name:    "wrong informational value",
			yaml:    strings.Replace(valid, "      - '🔵  go-docker-rolling-internal-pipeline.yml  🔵 🔵'", "      - other", 1),
			wantErr: "allowed value 0",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			err := ValidatePipelineParameterContract([]byte(test.yaml))
			if test.wantErr == "" {
				if err != nil {
					t.Fatal(err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), test.wantErr) {
				t.Fatalf("error = %v, want substring %q", err, test.wantErr)
			}
		})
	}
}

func TestValidateUnofficialPipelineParameterContract(t *testing.T) {
	valid := "parameters:\n" +
		"  - name: _info\n" +
		"    type: string\n" +
		"    values:\n" +
		"      - '🔵  go-docker-rolling-internal-pipeline-unofficial.yml  🔵 🔵'\n" +
		"    default: '🔵  go-docker-rolling-internal-pipeline-unofficial.yml  🔵 🔵'\n" +
		"  - name: sourceBuildPipelineRunId\n" +
		"    type: string\n" +
		"    default: '$(Build.BuildId)'\n" +
		"  - name: publishRepoPrefix\n" +
		"    type: string\n" +
		"    default: dev/\n"
	if err := ValidateUnofficialPipelineParameterContract([]byte(valid)); err != nil {
		t.Fatal(err)
	}
	unsafe := strings.Replace(valid, "default: dev/", "default: public/", 1)
	if err := ValidateUnofficialPipelineParameterContract([]byte(unsafe)); err == nil ||
		!strings.Contains(err.Error(), `expected "dev/"`) {

		t.Fatalf("unsafe contract error = %v", err)
	}
}
