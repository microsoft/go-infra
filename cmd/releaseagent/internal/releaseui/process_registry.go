// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package releaseui

import (
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"strconv"
	"strings"
)

var (
	processIDPattern = regexp.MustCompile(`^[a-z0-9]+(?:-[a-z0-9]+)*$`)
	inputIDPattern   = regexp.MustCompile(`^[a-z][A-Za-z0-9]*$`)
)

// ProcessDefinition describes one release process served at /{ID} through the shared process page.
type ProcessDefinition struct {
	ID               string
	Name             string
	Mark             string
	Description      string
	DocumentationURL string
	Workflow         *ProcessWorkflow
}

// ProcessWorkflow describes an in-UI workflow. DurableAction uses the shared confirmed execution
// lifecycle; custom server hooks remain available for workflows such as go-images.
type ProcessWorkflow struct {
	Heading       string         `json:"heading"`
	Description   string         `json:"description,omitempty"`
	SubmitLabel   string         `json:"submitLabel,omitempty"`
	Inputs        []ProcessInput `json:"inputs,omitempty"`
	DurableAction bool           `json:"-"`
	Preflight     ProcessHandler `json:"-"`
	GetPlan       ProcessHandler `json:"-"`
	Prepare       ProcessHandler `json:"-"`
	Simulate      ProcessHandler `json:"-"`
	Start         ProcessHandler `json:"-"`
}

// ProcessHandler implements process-specific planning or execution behind shared routes and UI.
type ProcessHandler func(*Server, http.ResponseWriter, *http.Request)

// ProcessInput describes one control rendered by the shared process page.
type ProcessInput struct {
	ID          string               `json:"id"`
	Type        string               `json:"type"`
	Label       string               `json:"label"`
	Description string               `json:"description,omitempty"`
	Default     string               `json:"default,omitempty"`
	Placeholder string               `json:"placeholder,omitempty"`
	Required    bool                 `json:"required,omitempty"`
	Options     []ProcessInputOption `json:"options,omitempty"`
	VisibleWhen *ProcessCondition    `json:"visibleWhen,omitempty"`
}

// ProcessInputOption describes one choice in a choice input.
type ProcessInputOption struct {
	Value       string `json:"value"`
	Name        string `json:"name"`
	Mark        string `json:"mark,omitempty"`
	Description string `json:"description"`
	NoticeTitle string `json:"noticeTitle,omitempty"`
	Notice      string `json:"notice,omitempty"`
}

// ProcessCondition conditionally displays an input based on another input's value.
type ProcessCondition struct {
	InputID string `json:"inputId"`
	Equals  string `json:"equals"`
}

type processRegistry struct {
	ordered []ProcessDefinition
	byID    map[string]ProcessDefinition
}

func newProcessRegistry(definitions ...ProcessDefinition) (*processRegistry, error) {
	if len(definitions) == 0 {
		return nil, errors.New("release process registry is empty")
	}
	registry := &processRegistry{
		ordered: make([]ProcessDefinition, 0, len(definitions)),
		byID:    make(map[string]ProcessDefinition),
	}
	ids := make(map[string]struct{}, len(definitions))
	for _, definition := range definitions {
		if !processIDPattern.MatchString(definition.ID) {
			return nil, fmt.Errorf("invalid release process ID %q", definition.ID)
		}
		if _, exists := ids[definition.ID]; exists {
			return nil, fmt.Errorf("duplicate release process ID %q", definition.ID)
		}
		ids[definition.ID] = struct{}{}
		if strings.TrimSpace(definition.Name) == "" || strings.TrimSpace(definition.Mark) == "" ||
			strings.TrimSpace(definition.Description) == "" {

			return nil, fmt.Errorf("release process %q has incomplete catalog metadata", definition.ID)
		}
		if definition.DocumentationURL != "" && !strings.HasPrefix(definition.DocumentationURL, "https://") {
			return nil, fmt.Errorf("release process %q has an invalid documentation URL", definition.ID)
		}
		if err := validateProcessWorkflow(definition.ID, definition.Workflow); err != nil {
			return nil, err
		}
		registry.byID[definition.ID] = definition
		registry.ordered = append(registry.ordered, definition)
	}
	return registry, nil
}

func defaultProcessRegistry() (*processRegistry, error) {
	return newProcessRegistry(
		ProcessDefinition{
			ID: "go-images", Name: "Go images", Mark: "GI",
			Description:      "Build, sign, publish, test, or republish the Microsoft Build of Go container images.",
			DocumentationURL: "https://github.com/microsoft/go-lab/tree/main/docs/release#golang-toolset-images",
			Workflow: &ProcessWorkflow{
				Heading: "Choose release type", Description: "Only rollback accepts a pipeline input.",
				SubmitLabel: "Prepare release",
				Inputs: []ProcessInput{
					{
						ID: "mode", Type: "choice", Label: "Release type", Default: "normal", Required: true,
						Options: []ProcessInputOption{
							{Value: "normal", Name: "Normal release", Mark: "N", Description: "Build current microsoft/main and publish to public/. All parameters are locked.", NoticeTitle: "Normal release is locked.", Notice: "Current main, current-build artifacts, and public/ are selected server-side."},
							{Value: "rollback", Name: "Rollback / republish", Mark: "R", Description: "Republish artifacts from one successful pipeline 1023 build to public/.", NoticeTitle: "Only the source build is editable.", Notice: "The server locks current main and public/, then validates the selected build."},
							{Value: "test", Name: "Test release", Mark: "T", Description: "Build current microsoft/main and publish only under the dev/ prefix.", NoticeTitle: "Test release is locked to dev/.", Notice: "Current main is built normally, but publication is isolated under the dev/ prefix."},
						},
					},
					{
						ID: "sourceBuildId", Type: "number", Label: "Source build ID", Required: true,
						Placeholder: "3034159",
						Description: "The server verifies that this is a successful pipeline 1023 run which produced its own artifacts.",
						VisibleWhen: &ProcessCondition{InputID: "mode", Equals: "rollback"},
					},
				},
				Preflight: (*Server).handlePreflight,
				GetPlan:   (*Server).handleGetPlan,
				Prepare:   (*Server).handlePlan,
				Simulate:  (*Server).handleDemoStart,
				Start:     (*Server).handleReleaseStart,
			},
		},
		ProcessDefinition{
			ID: "go-infra", Name: "Go infrastructure", Mark: "IN",
			Description:      "Create the next microsoft/go-infra patch release through its GitHub release workflow.",
			DocumentationURL: "https://github.com/microsoft/go-lab/tree/main/docs/release#microsoftgo-infra",
			Workflow: &ProcessWorkflow{
				Heading: "Choose release path", Description: "Review a fixed GitHub action before confirming it.",
				SubmitLabel: "Review GitHub action",
				Inputs: []ProcessInput{
					{
						ID: "action", Type: "choice", Label: "Release path", Default: goInfraActionReleaseOnMerge, Required: true,
						Options: []ProcessInputOption{
							{
								Value: goInfraActionReleaseOnMerge, Name: "Release on merge", Mark: "PR",
								Description: "Add release-on-merge to one open, non-fork PR targeting main.",
								NoticeTitle: "The UI does not merge the PR.",
								Notice:      "The existing workflow creates the patch release only after the labeled PR is merged.",
							},
							{
								Value: goInfraActionManualDispatch, Name: "Manual workflow dispatch", Mark: "WD",
								Description: "Dispatch the fixed patch-release workflow on main as a dry run or publish action.",
								NoticeTitle: "Publishing requires a second confirmation.",
								Notice:      "Dry run only calculates the next version; publish can create the next patch release.",
							},
						},
					},
					{
						ID: "pullRequest", Type: "number", Label: "Pull request number", Required: true,
						Placeholder: "123",
						Description: "The server verifies that the PR is open, targets main, and does not come from a fork.",
						VisibleWhen: &ProcessCondition{InputID: "action", Equals: goInfraActionReleaseOnMerge},
					},
					{
						ID: "dispatchMode", Type: "choice", Label: "Dispatch mode", Default: goInfraDispatchModeDryRun, Required: true,
						VisibleWhen: &ProcessCondition{InputID: "action", Equals: goInfraActionManualDispatch},
						Options: []ProcessInputOption{
							{Value: goInfraDispatchModeDryRun, Name: "Dry run", Mark: "D", Description: "Calculate the next v0.0.x version without creating a release."},
							{Value: goInfraDispatchModePublish, Name: "Publish release", Mark: "P", Description: "Run the workflow on main and create the next patch release."},
						},
					},
				},
				DurableAction: true,
			},
		},
	)
}

func validateProcessWorkflow(processID string, workflow *ProcessWorkflow) error {
	if workflow == nil {
		return fmt.Errorf("release process %q has no workflow", processID)
	}
	if strings.TrimSpace(workflow.Heading) == "" {
		return fmt.Errorf("release process %q has an incomplete workflow", processID)
	}
	if workflow.DurableAction && (workflow.GetPlan != nil || workflow.Prepare != nil || workflow.Simulate != nil || workflow.Start != nil) {
		return fmt.Errorf("release process %q mixes durable execution with custom lifecycle handlers", processID)
	}
	if (workflow.Prepare == nil) != (workflow.GetPlan == nil) {
		return fmt.Errorf("release process %q must define prepare and get-plan handlers together", processID)
	}
	if (workflow.Prepare != nil || workflow.DurableAction) && strings.TrimSpace(workflow.SubmitLabel) == "" {
		return fmt.Errorf("release process %q has no workflow submit label", processID)
	}
	if workflow.Simulate != nil && workflow.Prepare == nil {
		return fmt.Errorf("release process %q simulates without defining preparation", processID)
	}
	if workflow.Start != nil && workflow.Prepare == nil {
		return fmt.Errorf("release process %q starts without defining preparation", processID)
	}
	inputs := make(map[string]ProcessInput, len(workflow.Inputs))
	for _, input := range workflow.Inputs {
		if !inputIDPattern.MatchString(input.ID) || strings.TrimSpace(input.Label) == "" ||
			input.Type != "choice" && input.Type != "number" {

			return fmt.Errorf("release process %q has an invalid input %q", processID, input.ID)
		}
		if _, exists := inputs[input.ID]; exists {
			return fmt.Errorf("release process %q repeats input %q", processID, input.ID)
		}
		if (input.Type == "choice" && len(input.Options) == 0) ||
			(input.Type != "choice" && len(input.Options) != 0) {

			return fmt.Errorf("release process %q input %q has invalid options", processID, input.ID)
		}
		optionValues := make(map[string]struct{}, len(input.Options))
		for _, option := range input.Options {
			if strings.TrimSpace(option.Value) == "" || strings.TrimSpace(option.Name) == "" || strings.TrimSpace(option.Description) == "" {
				return fmt.Errorf("release process %q input %q has an invalid option", processID, input.ID)
			}
			if _, exists := optionValues[option.Value]; exists {
				return fmt.Errorf("release process %q input %q repeats option %q", processID, input.ID, option.Value)
			}
			optionValues[option.Value] = struct{}{}
		}
		if input.Default != "" && input.Type == "choice" {
			if _, exists := optionValues[input.Default]; !exists {
				return fmt.Errorf("release process %q input %q has an invalid default", processID, input.ID)
			}
		}
		if input.Default != "" && input.Type == "number" {
			value, err := strconv.ParseUint(input.Default, 10, 64)
			if err != nil || value == 0 {
				return fmt.Errorf("release process %q input %q has an invalid default", processID, input.ID)
			}
		}
		inputs[input.ID] = input
	}
	for _, input := range workflow.Inputs {
		if input.VisibleWhen == nil {
			continue
		}
		controlling, exists := inputs[input.VisibleWhen.InputID]
		if !exists || controlling.Type != "choice" {
			return fmt.Errorf("release process %q input %q has an invalid condition", processID, input.ID)
		}
		matched := false
		for _, option := range controlling.Options {
			matched = matched || option.Value == input.VisibleWhen.Equals
		}
		if !matched {
			return fmt.Errorf("release process %q input %q has an invalid condition value", processID, input.ID)
		}
	}
	return nil
}

func processPath(id string) string {
	return "/" + id
}

func (r *processRegistry) page(path string) (string, bool) {
	id := strings.TrimPrefix(path, "/")
	_, ok := r.byID[id]
	ok = ok && path == processPath(id)
	return "process.html", ok
}

func (r *processRegistry) process(id string) (ProcessDefinition, bool) {
	definition, ok := r.byID[id]
	return definition, ok
}

func (r *processRegistry) summaries() []processSummary {
	summaries := make([]processSummary, 0, len(r.ordered))
	for _, definition := range r.ordered {
		summaries = append(summaries, processSummary{
			ID: definition.ID, Name: definition.Name, Mark: definition.Mark,
			Description: definition.Description, Href: processPath(definition.ID),
		})
	}
	return summaries
}
