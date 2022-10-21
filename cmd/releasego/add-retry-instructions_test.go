// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package main

import (
	"path/filepath"
	"testing"

	"github.com/microsoft/go-infra/goldentest"
)

func Test_generateContent(t *testing.T) {
	tests := []struct {
		name string
		args retryTemplateArgs
	}{
		{"empty", retryTemplateArgs{}},
		{"empty-checkboxes", retryTemplateArgs{Checkboxes: true}},
		{"empty-preapproval", retryTemplateArgs{Preapproval: true}},

		{"go1", retryTemplateArgs{LastNonNilEnv: mustNewEnvArg("poll1MicrosoftGoPRNumber=42")}},
		{"go1-capitalized", retryTemplateArgs{LastNonNilEnv: mustNewEnvArg("POLL1MICROSOFTGOPRNUMBER=42")}},
		{"go2", retryTemplateArgs{LastNonNilEnv: mustNewEnvArg("poll2MicrosoftGoCommitHash=2004985")}},
		{"go3", retryTemplateArgs{LastNonNilEnv: mustNewEnvArg("poll3MicrosoftGoBuildID=2004985")}},
		{"go4", retryTemplateArgs{LastNonNilEnv: mustNewEnvArg("poll4MicrosoftGoImagesPRNumber=8")}},

		{"images1", retryTemplateArgs{LastNonNilEnv: mustNewEnvArg("poll1MicrosoftGoImagesCommitHash=42abcdef")}},
		{"images2", retryTemplateArgs{LastNonNilEnv: mustNewEnvArg("poll2MicrosoftGoImagesBuildID=1987093")}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := generateContent(tt.args)
			if err != nil {
				t.Errorf("generateContent() error = %v", err)
				return
			}
			goldentest.Check(t, "Test_generateContent ", filepath.Join("testdata", "retry-instructions", tt.name+".golden.md"), got)
		})
	}
}

func mustNewEnvArg(env string) *envArg {
	arg, err := newEnvArg(env)
	if err != nil {
		panic(err)
	}
	if arg == nil {
		panic("no envArg for " + env)
	}
	return arg
}
