// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package main

import (
	"fmt"
	"io"
	"strings"
	"testing"
)

func TestParseConfig(t *testing.T) {
	env := map[string]string{
		"GITHUB_REPOSITORY":   "microsoft/agent-framework-go",
		"GITHUB_TOKEN":        "token",
		"GITHUB_STEP_SUMMARY": "/tmp/summary",
	}
	cfg, err := parseConfig([]string{
		"-pr-number", "42",
		"-area-prefixes", `{"area:provider/openai":["provider\\openaiprovider/","provider/openaiprovider"]}`,
	}, func(name string) string { return env[name] }, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Owner != "microsoft" || cfg.Repo != "agent-framework-go" || cfg.PRNumber != 42 {
		t.Fatalf("unexpected config: %#v", cfg)
	}
	wantAreas := []string{"provider/openaiprovider"}
	if len(cfg.AreaPrefixes["area:provider/openai"]) != 1 ||
		cfg.AreaPrefixes["area:provider/openai"][0] != wantAreas[0] {

		t.Fatalf("area prefixes = %#v, want %#v", cfg.AreaPrefixes, wantAreas)
	}
	if cfg.Token != "token" || cfg.SummaryPath != "/tmp/summary" {
		t.Fatalf("environment-backed fields not populated: %#v", cfg)
	}
}

func TestParseConfigRejectsInvalidValues(t *testing.T) {
	env := func(name string) string {
		if name == "GITHUB_TOKEN" {
			return "token"
		}
		return ""
	}
	tests := []struct {
		name string
		args []string
	}{
		{name: "repo", args: []string{"-repo", "invalid"}},
		{name: "negative-pr", args: []string{"-repo", "o/r", "-pr-number", "-1"}},
		{name: "area-label", args: []string{"-repo", "o/r", "-area-prefixes", `{"not-an-area":["src"]}`}},
		{name: "empty-area-prefix", args: []string{"-repo", "o/r", "-area-prefixes", `{"area:src":["/"]}`}},
		{name: "parent-area-prefix", args: []string{"-repo", "o/r", "-area-prefixes", `{"area:src":["../src"]}`}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := parseConfig(tt.args, env, io.Discard); err == nil {
				t.Fatal("expected an error")
			}
		})
	}
}

func TestParseAreaPrefixesRejectsTooManyAreas(t *testing.T) {
	entries := make([]string, maxAreaLabels+1)
	for i := range entries {
		entries[i] = fmt.Sprintf(`"area:a%d":["path%d"]`, i, i)
	}
	if _, err := parseAreaPrefixes("{" + strings.Join(entries, ",") + "}"); err == nil {
		t.Fatal("expected an error")
	}
}
