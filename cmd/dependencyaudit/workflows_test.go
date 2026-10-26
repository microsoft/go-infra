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
		If      string
		Env     map[string]string
		Outputs map[string]string
		With    map[string]string
		Steps   []workflowStep
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

func TestReusableWorkflowContracts(t *testing.T) {
	router := loadWorkflow(t, "dependency-audit-target.yml")
	prepare := router.Jobs["prepare"]
	if prepare.Outputs["control_sha"] != "${{ job.workflow_sha }}" {
		t.Fatal("the helper revision must come from the called workflow, not the caller")
	}
	foundCheckout := false
	for _, step := range prepare.Steps {
		if strings.HasPrefix(step.Uses, "actions/checkout@") {
			foundCheckout = true
			if step.With["repository"] != "microsoft/go-infra" || step.With["ref"] != prepare.Outputs["control_sha"] ||
				step.With["persist-credentials"] != "false" {

				t.Fatal("preparation must use the pinned go-infra source without persisting credentials")
			}
		}
	}
	if !foundCheckout {
		t.Fatal("preparation is missing the go-infra checkout")
	}
	enrollment := `${{ github.repository != 'microsoft/go-infra' && format('["{0}"]', github.repository) || vars.DEPENDENCY_AUDIT_REPOSITORIES }}`
	if prepare.Env["DEPENDENCY_AUDIT_REPOSITORIES"] != enrollment {
		t.Fatal("external callers must be restricted to their own repository; central runs require enrollment")
	}
	for _, mode := range []string{"audit", "apply"} {
		t.Run(mode, func(t *testing.T) {
			call := router.Jobs[mode]
			if call.With["control_repo"] != "microsoft/go-infra" ||
				call.With["control_sha"] != "${{ needs.prepare.outputs.control_sha }}" {

				t.Fatal("publication must use the same trusted helper revision as preparation")
			}
			name := "dependency-audit-agent.md"
			if mode == "apply" {
				name = "dependency-audit-apply-agent.md"
			}
			foundEnrollment := false
			for _, step := range loadWorkflow(t, name).SafeOutputs.Steps {
				if value, ok := step.Env["DEPENDENCY_AUDIT_REPOSITORIES"]; ok {
					foundEnrollment = true
					if value != enrollment {
						t.Fatal("publication must preserve the caller's target restriction")
					}
				}
			}
			if !foundEnrollment {
				t.Fatal("publication must recheck target enrollment")
			}
		})
	}
}

func TestTemporaryPRAuditGate(t *testing.T) {
	router := loadWorkflow(t, "dependency-audit-target.yml")
	gate := workflowStepByID(t, router.Jobs["prepare"].Steps, "caller")
	if gate.If != "github.event.repository.fork == true || github.ref != format('refs/heads/{0}', github.event.repository.default_branch)" {
		t.Fatal("the normal default-branch and fork guard must remain unchanged")
	}
	for _, test := range []struct {
		name, key, value string
		allowed          bool
	}{
		{name: "trusted-labeled-audit", allowed: true},
		{name: "fork-repository", key: "REPOSITORY_FORK", value: "true"},
		{name: "different-repository", key: "CALLER_REPOSITORY", value: "other/repo"},
		{name: "different-ref", key: "CALLER_REF", value: "refs/heads/feature"},
		{name: "different-event", key: "EVENT_NAME", value: "workflow_dispatch"},
		{name: "push-update", key: "EVENT_ACTION", value: "synchronize"},
		{name: "different-label", key: "EVENT_LABEL", value: "other-label"},
		{name: "different-pr", key: "PR_NUMBER", value: "1324"},
		{name: "fork-pr", key: "PR_HEAD_REPOSITORY", value: "other/agent-framework-go"},
		{name: "different-head-branch", key: "PR_HEAD_BRANCH", value: "other-branch"},
		{name: "different-base-branch", key: "PR_BASE_BRANCH", value: "other-branch"},
		{name: "apply", key: "MODE", value: "apply"},
		{name: "different-target", key: "TARGET", value: "other/repo"},
	} {
		t.Run(test.name, func(t *testing.T) {
			env := map[string]string{
				"CALLER_REPOSITORY":  "microsoft/agent-framework-go",
				"CALLER_REF":         "refs/pull/1323/merge",
				"REPOSITORY_FORK":    "false",
				"EVENT_NAME":         "pull_request",
				"EVENT_ACTION":       "labeled",
				"EVENT_LABEL":        "dependency-audit-smoke",
				"PR_NUMBER":          "1323",
				"PR_HEAD_REPOSITORY": "microsoft/agent-framework-go",
				"PR_HEAD_BRANCH":     "dev/mclayton/dependency-audit-consumer",
				"PR_BASE_BRANCH":     "main",
				"DEFAULT_BRANCH":     "main",
				"MODE":               "audit",
				"TARGET":             "microsoft/agent-framework-go",
			}
			if test.key != "" {
				env[test.key] = test.value
			}
			command := exec.CommandContext(t.Context(), "bash", "-euo", "pipefail", "-c", gate.Run)
			command.Env = os.Environ()
			for key, value := range env {
				command.Env = append(command.Env, key+"="+value)
			}
			output, err := command.CombinedOutput()
			if (err == nil) != test.allowed {
				t.Fatalf("PR audit gate for %s: %s, %v", test.name, output, err)
			}
		})
	}
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
	for _, test := range []struct {
		name, bundle, extra, failure string
		mismatch                     bool
	}{
		{name: "patch-only"},
		{name: "matching-tree", bundle: "aw-test.bundle"},
		{name: "mismatched-tree", bundle: "aw-test.bundle", mismatch: true, failure: "does not match"},
		{name: "independently-named-bundle", bundle: "aw-o-r-test.bundle", mismatch: true, failure: "Unmatched or additional"},
		{name: "independently-named-identical-tree", bundle: "aw-o-r-test.bundle", failure: "Unmatched or additional"},
		{name: "additional-bundle", bundle: "aw-test.bundle", extra: "aw-o-r-test.bundle", failure: "Unmatched or additional"},
	} {
		t.Run(test.name, func(t *testing.T) {
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
			if test.mismatch {
				writeFile(t, filepath.Join(target, "use.go"), "package test\nfunc use() { println(2) }\n")
			}
			testGit(t, target, "add", ".")
			testGit(t, target, "commit", "-qm", "migration")
			for _, bundle := range []string{test.bundle, test.extra} {
				if bundle != "" {
					testGit(t, target, "bundle", "create", filepath.Join(artifacts, bundle), base+"..HEAD")
				}
			}
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
			if test.failure != "" {
				if err == nil || !strings.Contains(string(output), test.failure) {
					t.Fatalf("invalid native bundle passed: %s, %v", output, err)
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
