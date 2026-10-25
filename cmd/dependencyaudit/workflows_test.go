// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"go.yaml.in/yaml/v4"
)

type workflowStep struct {
	ID, If, Run, Uses string
	Env               map[string]string
	With              map[string]string
}

type workflow struct {
	Checkout    struct{ Ref string }
	SafeOutputs struct {
		Needs []string
		Steps []workflowStep
		Push  any `yaml:"push-to-pull-request-branch"`
	} `yaml:"safe-outputs"`
	Jobs map[string]struct {
		If    string
		Steps []workflowStep
	}
}

func loadWorkflow(t *testing.T, name string) workflow {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("..", "..", ".github", "workflows", name))
	if err != nil {
		t.Fatal(err)
	}
	if strings.HasSuffix(name, ".md") {
		parts := strings.SplitN(string(data), "---", 3)
		if len(parts) != 3 {
			t.Fatalf("%s is missing workflow frontmatter", name)
		}
		data = []byte(parts[1])
	}
	var result workflow
	if err := yaml.Unmarshal(data, &result); err != nil {
		t.Fatal(err)
	}
	return result
}

func workflowStepByID(t *testing.T, steps []workflowStep, id string) workflowStep {
	t.Helper()
	for _, step := range steps {
		if step.ID == id {
			return step
		}
	}
	t.Fatalf("workflow step %q is missing", id)
	return workflowStep{}
}

func TestApplyWorkflowContracts(t *testing.T) {
	router := loadWorkflow(t, "dependency-audit-target.yml")
	if !strings.Contains(router.Jobs["apply"].If, "needs.prepare.outputs.pull == '0'") {
		t.Fatal("existing PRs must bypass the fix agent")
	}
	linker := workflowStepByID(t, router.Jobs["prepare"].Steps, "linker")
	link := workflowStepByID(t, router.Jobs["prepare"].Steps, "link")
	if !strings.Contains(link.If, "steps.context.outputs.pull_request != '0'") || linker.If != link.If {
		t.Fatal("link-only access must only be requested for an existing PR")
	}
	if linker.With["permission-issues"] != "write" || linker.With["permission-pull-requests"] != "read" ||
		linker.With["permission-contents"] == "write" {

		t.Fatal("link-only access must not permit code or PR changes")
	}
	apply := loadWorkflow(t, "dependency-audit-apply-agent.md")
	if apply.Checkout.Ref != "${{ inputs.head }}" || apply.SafeOutputs.Push != nil {
		t.Fatal("fixes must use the audited head and never update existing branches")
	}
	foundCheckout := false
	for _, step := range apply.Jobs["validate"].Steps {
		if strings.HasPrefix(step.Uses, "actions/checkout@") {
			foundCheckout = true
			if step.With["ref"] != "${{ inputs.head }}" {
				t.Fatal("validation must use the same audited head")
			}
		}
	}
	if !foundCheckout || !slices.Contains(apply.SafeOutputs.Needs, "validate") {
		t.Fatal("publication must depend on independent validation")
	}
	gate := workflowStepByID(t, apply.SafeOutputs.Steps, "require-validation")
	for _, outcome := range []string{"success", "failure", "skipped"} {
		command := exec.CommandContext(t.Context(), "bash", "-euo", "pipefail", "-c", gate.Run)
		command.Env = append(os.Environ(), "VALIDATION="+outcome)
		output, err := command.CombinedOutput()
		if (err == nil) != (outcome == "success") {
			t.Fatalf("publication gate for %s: %s, %v", outcome, output, err)
		}
	}
}

func testGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	command := exec.CommandContext(t.Context(), "git", append([]string{"-c", "core.hooksPath=/dev/null", "-C", dir}, args...)...)
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %s: %v", args, output, err)
	}
	return strings.TrimSpace(string(output))
}

func writeFile(t *testing.T, name, body string) {
	t.Helper()
	if err := os.WriteFile(name, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestNativePatchAndBundleValidation(t *testing.T) {
	flow := loadWorkflow(t, "dependency-audit-apply-agent.md")
	script := workflowStepByID(t, flow.Jobs["validate"].Steps, "validate-patch").Run
	for _, mismatch := range []bool{false, true} {
		t.Run(map[bool]string{false: "matching-tree", true: "mismatched-tree"}[mismatch], func(t *testing.T) {
			root := t.TempDir()
			target, artifacts, bin := filepath.Join(root, "target"), filepath.Join(root, "agent"), filepath.Join(root, "bin")
			for _, dir := range []string{target, artifacts, bin} {
				if err := os.Mkdir(dir, 0o755); err != nil {
					t.Fatal(err)
				}
			}
			testGit(t, target, "init", "-b", "main")
			testGit(t, target, "config", "user.name", "Audit test")
			testGit(t, target, "config", "user.email", "audit@example.test")
			testGit(t, target, "config", "commit.gpgsign", "false")
			writeFile(t, filepath.Join(target, "go.mod"), "module example.com/test\n")
			writeFile(t, filepath.Join(target, "use.go"), "package test\nfunc use() {}\n")
			testGit(t, target, "add", ".")
			testGit(t, target, "commit", "-qm", "base")
			base := testGit(t, target, "rev-parse", "HEAD")
			writeFile(t, filepath.Join(target, "use.go"), "package test\nfunc use() { println(1) }\n")
			patch := filepath.Join(artifacts, "aw-test.patch")
			writeFile(t, patch, testGit(t, target, "diff", "--binary")+"\n")
			if mismatch {
				writeFile(t, filepath.Join(target, "use.go"), "package test\nfunc use() { println(2) }\n")
			}
			testGit(t, target, "add", ".")
			testGit(t, target, "commit", "-qm", "migration")
			testGit(t, target, "bundle", "create", filepath.Join(artifacts, "aw-test.bundle"), base+"..HEAD")
			testGit(t, target, "checkout", "--detach", base)
			docker := filepath.Join(bin, "docker")
			writeFile(t, docker, "#!/bin/sh\nprintf '%s\\n' \"$*\" >> \"$DOCKER_LOG\"\n")
			if err := os.Chmod(docker, 0o755); err != nil {
				t.Fatal(err)
			}
			log := filepath.Join(root, "docker.log")
			command := exec.CommandContext(t.Context(), "bash", "-euo", "pipefail", "-c", script)
			command.Dir = root
			command.Env = append(os.Environ(), "PATH="+bin+string(os.PathListSeparator)+os.Getenv("PATH"),
				"RUNNER_TEMP="+root, "GITHUB_WORKSPACE="+root, "TMPDIR="+root,
				"MODULE=.", "TARGET_KEY=test", "GITHUB_RUN_ID=1", "DOCKER_LOG="+log)
			output, err := command.CombinedOutput()
			if mismatch {
				if err == nil || !strings.Contains(string(output), "does not match") {
					t.Fatalf("mismatched native bundle passed: %s, %v", output, err)
				}
				if _, err := os.Stat(log); !os.IsNotExist(err) {
					t.Fatal("tests must not run against a different bundle tree")
				}
				return
			}
			if err != nil {
				t.Fatalf("validation script failed: %s, %v", output, err)
			}
			calls, err := os.ReadFile(log)
			if err != nil {
				t.Fatal(err)
			}
			for _, required := range []string{"--network=none", "--cap-drop=ALL", "--user 65534:65534", "target=/workspace,readonly", "go vet ./... && go test ./... && go build ./..."} {
				if !strings.Contains(string(calls), required) {
					t.Fatalf("validation lost isolation option %q", required)
				}
			}
			if strings.Contains(string(calls), "TOKEN") || strings.Contains(string(calls), "docker.sock") {
				t.Fatal("target tests received credentials or a Docker socket")
			}
		})
	}
}
