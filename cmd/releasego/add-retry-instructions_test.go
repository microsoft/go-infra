package main

import (
	"flag"
	"os"
	"path/filepath"
	"testing"
)

var update = flag.Bool("update", false, "Update the golden files instead of failing.")

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
			checkGolden(t, filepath.Join("testdata", "retry-instructions", tt.name+".golden.md"), got)
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

const regenGoldenHelp = "Run 'go test ./cmd/releasego -run Test_generateContent -update' to update golden file"

func checkGolden(t *testing.T, goldenPath string, actual string) {
	if *update {
		if err := os.MkdirAll(filepath.Dir(goldenPath), os.ModePerm); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(goldenPath, []byte(actual), 0666); err != nil {
			t.Fatal(err)
		}
	}

	want, err := os.ReadFile(goldenPath)
	if err != nil {
		t.Fatalf("Unable to read golden file. %v. Error: %v", regenGoldenHelp, err)
	}

	if actual != string(want) {
		t.Errorf("Actual result didn't match golden file. %v and examine the Git diff to determine if the change is acceptable.", regenGoldenHelp)
	}
}
