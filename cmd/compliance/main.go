// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

// Compliance copies answers from prior completed Azure DevOps compliance
// assessments and optionally generates the completion work-item session.
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"os/signal"
	"strings"
	"syscall"
	"text/tabwriter"
	"time"
)

const azureDevOpsResource = "499b84ac-1321-427f-aa17-267ca6975798"

type portalTarget struct {
	Organization string
	Project      string
	ProductID    string
}

type cliOptions struct {
	PortalURL          string
	AssessmentGroup    string
	SourceGroup        string
	AnswersFile        string
	CompleteActivities stringListFlag
	APIBase            string
	AzureCLI           string
	Apply              bool
	AnswersOnly        bool
	AllowPartial       bool
	Overwrite          bool
	Timeout            time.Duration
}

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	if err := runCLI(ctx, os.Args[1:], os.Stdout, os.Stderr); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return
		}
		fmt.Fprintf(os.Stderr, "compliance: %v\n", err)
		os.Exit(1)
	}
}

func runCLI(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	options, err := parseFlags(args, stderr)
	if err != nil {
		return err
	}
	target, err := parsePortalURL(options.PortalURL)
	if err != nil {
		return err
	}
	if options.Apply && options.AssessmentGroup == "" {
		return errors.New("-assessment-group is required with -apply; run a dry run first to identify the target group")
	}
	if options.APIBase == "" {
		options.APIBase = "https://entreq.dev.azure.com/" + url.PathEscape(strings.ToLower(target.Organization))
	} else if parsed, err := url.Parse(options.APIBase); err != nil || parsed.Scheme != "https" || parsed.Host == "" {
		return errors.New("-api-base must be an absolute HTTPS URL")
	}
	answerOverrides, err := loadAnswerOverrides(options.AnswersFile)
	if err != nil {
		return err
	}
	completeActivities, err := parseCompleteActivities(options.CompleteActivities)
	if err != nil {
		return err
	}

	ctx, cancel := context.WithTimeout(ctx, options.Timeout)
	defer cancel()
	token, err := acquireAzureDevOpsToken(ctx, options.AzureCLI)
	if err != nil {
		return err
	}
	client, err := newComplianceClient(options.APIBase, token, &http.Client{Timeout: time.Minute})
	if err != nil {
		return err
	}

	s, err := client.getScope(ctx, target.ProductID)
	if err != nil {
		return fmt.Errorf("read product %s assessments: %w", target.ProductID, err)
	}
	targetGroup, err := selectTargetGroup(s.AssessmentGroups, options.AssessmentGroup)
	if err != nil {
		return err
	}
	for _, assessment := range targetGroup.Assessments {
		if assessment.LastSession == nil {
			continue
		}
		session, err := client.getSession(ctx, assessment.LastSession.SessionID)
		if err != nil {
			return fmt.Errorf("read existing session for assessment %q: %w", assessment.Name, err)
		}
		assessment.LastSession.Session = session
	}
	plan, err := buildCompletionPlan(s, options.AssessmentGroup, options.SourceGroup, options.Overwrite)
	if err != nil {
		return err
	}
	assessmentNames := make(map[string]struct{}, len(plan.Items))
	for _, item := range plan.Items {
		assessmentNames[item.Target.Name] = struct{}{}
	}
	for name := range answerOverrides {
		if _, ok := assessmentNames[name]; !ok {
			return fmt.Errorf("-answers-file contains unknown assessment %q", name)
		}
	}
	for name := range completeActivities {
		if _, ok := assessmentNames[name]; !ok {
			return fmt.Errorf("-complete-activity contains unknown assessment %q", name)
		}
	}
	results, runErr := runCompletionPlan(ctx, client, s, plan, completionOptions{
		Apply:              options.Apply,
		AnswersOnly:        options.AnswersOnly,
		AllowPartial:       options.AllowPartial,
		AnswerOverrides:    answerOverrides,
		CompleteActivities: completeActivities,
		PollInterval:       2 * time.Second,
	})
	writeCompletionResults(stdout, target, plan, results, options)
	return runErr
}

func parseFlags(args []string, stderr io.Writer) (cliOptions, error) {
	var options cliOptions
	fs := flag.NewFlagSet("compliance", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.StringVar(&options.PortalURL, "url", "", "Azure DevOps compliance assessments URL (required)")
	fs.StringVar(&options.AssessmentGroup, "assessment-group", "", "Target assessment group; defaults to the newest group for dry runs")
	fs.StringVar(&options.SourceGroup, "source-group", "", "Only copy from this prior assessment group")
	fs.StringVar(&options.AnswersFile, "answers-file", "", "JSON file containing complete per-assessment answer overrides")
	fs.Var(&options.CompleteActivities, "complete-activity", "Current-only activity to complete, formatted as ASSESSMENT=NODE-ID; may be repeated")
	fs.StringVar(&options.APIBase, "api-base", "", "Override the Entreq API base URL")
	fs.StringVar(&options.AzureCLI, "az", "az", "Path to the Azure CLI executable")
	fs.BoolVar(&options.Apply, "apply", false, "Save answers and generate completion work items")
	fs.BoolVar(&options.AnswersOnly, "answers-only", false, "Save answers without generating a completion session")
	fs.BoolVar(&options.AllowPartial, "allow-partial", false, "Apply even when prior answers are incompatible with the current questionnaire")
	fs.BoolVar(&options.Overwrite, "overwrite", false, "Replace answers in assessments that are already in progress")
	fs.DurationVar(&options.Timeout, "timeout", 10*time.Minute, "Overall command timeout")
	fs.Usage = func() {
		fmt.Fprintf(fs.Output(), "Usage: %s -url URL [-assessment-group NAME] [-apply]\n\n", fs.Name())
		fmt.Fprintln(fs.Output(), "Copies answers from the latest earlier completed assessment for each policy.")
		fmt.Fprintln(fs.Output(), "The default is a read-only dry run. -apply can create or update Azure DevOps work items.")
		fmt.Fprintln(fs.Output(), "\nOptions:")
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		return cliOptions{}, err
	}
	if fs.NArg() != 0 {
		return cliOptions{}, fmt.Errorf("unexpected positional arguments: %s", strings.Join(fs.Args(), " "))
	}
	if options.PortalURL == "" {
		return cliOptions{}, errors.New("-url is required")
	}
	if options.Timeout <= 0 {
		return cliOptions{}, errors.New("-timeout must be greater than zero")
	}
	if options.AnswersOnly && !options.Apply {
		return cliOptions{}, errors.New("-answers-only requires -apply")
	}
	if options.AllowPartial && !options.Apply {
		return cliOptions{}, errors.New("-allow-partial requires -apply")
	}
	if options.Overwrite && !options.Apply {
		return cliOptions{}, errors.New("-overwrite requires -apply")
	}
	return options, nil
}

type stringListFlag []string

func (f *stringListFlag) String() string {
	return strings.Join(*f, ",")
}

func (f *stringListFlag) Set(value string) error {
	*f = append(*f, value)
	return nil
}

func parseCompleteActivities(values []string) (map[string]map[string]struct{}, error) {
	if len(values) == 0 {
		return nil, nil
	}
	result := make(map[string]map[string]struct{})
	for _, value := range values {
		assessmentName, nodeID, ok := strings.Cut(value, "=")
		assessmentName = strings.TrimSpace(assessmentName)
		nodeID = strings.TrimSpace(nodeID)
		if !ok || assessmentName == "" || nodeID == "" {
			return nil, fmt.Errorf("invalid -complete-activity %q; want ASSESSMENT=NODE-ID", value)
		}
		if result[assessmentName] == nil {
			result[assessmentName] = make(map[string]struct{})
		}
		result[assessmentName][nodeID] = struct{}{}
	}
	return result, nil
}

func loadAnswerOverrides(path string) (map[string][]json.RawMessage, error) {
	if path == "" {
		return nil, nil
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read -answers-file: %w", err)
	}
	if len(data) > 1<<20 {
		return nil, errors.New("-answers-file exceeds 1 MiB")
	}
	var overrides map[string][]json.RawMessage
	if err := json.Unmarshal(data, &overrides); err != nil {
		return nil, fmt.Errorf("decode -answers-file: %w", err)
	}
	if len(overrides) == 0 {
		return nil, errors.New("-answers-file contains no assessments")
	}
	for name, answers := range overrides {
		if strings.TrimSpace(name) == "" {
			return nil, errors.New("-answers-file contains an empty assessment name")
		}
		if len(answers) == 0 {
			return nil, fmt.Errorf("-answers-file contains no answers for assessment %q", name)
		}
	}
	return overrides, nil
}

func parsePortalURL(value string) (portalTarget, error) {
	parsed, err := url.Parse(value)
	if err != nil {
		return portalTarget{}, fmt.Errorf("parse -url: %w", err)
	}
	if parsed.Scheme != "https" || !strings.EqualFold(parsed.Hostname(), "dev.azure.com") {
		return portalTarget{}, errors.New("-url must be an https://dev.azure.com compliance URL")
	}
	segments := strings.Split(strings.Trim(parsed.Path, "/"), "/")
	for index := 2; index+3 < len(segments); index++ {
		if !strings.EqualFold(segments[index], "_compliance") || !strings.EqualFold(segments[index+1], "product") {
			continue
		}
		if segments[index+2] == "" || !strings.EqualFold(segments[index+3], "assessments") {
			break
		}
		return portalTarget{
			Organization: segments[0],
			Project:      segments[1],
			ProductID:    segments[index+2],
		}, nil
	}
	return portalTarget{}, errors.New("-url must end in /_compliance/product/{product-id}/assessments")
}

func acquireAzureDevOpsToken(ctx context.Context, azureCLI string) (string, error) {
	if pat := strings.TrimSpace(os.Getenv("AZURE_DEVOPS_EXT_PAT")); pat != "" {
		return pat, nil
	}

	command := exec.CommandContext(ctx, azureCLI,
		"account", "get-access-token",
		"--resource", azureDevOpsResource,
		"--query", "accessToken",
		"--output", "tsv",
	)
	var stderr bytes.Buffer
	command.Stderr = &stderr
	output, err := command.Output()
	if err != nil {
		return "", fmt.Errorf("acquire Azure DevOps token with %q: %w; run 'az login' or set AZURE_DEVOPS_EXT_PAT", azureCLI, err)
	}
	token := strings.TrimSpace(string(output))
	if token == "" {
		return "", errors.New("access token returned by Azure CLI is empty")
	}
	return token, nil
}

func writeCompletionResults(w io.Writer, target portalTarget, plan *completionPlan, results []completionResult, options cliOptions) {
	mode := "DRY RUN"
	if options.Apply {
		mode = "APPLY"
	}
	fmt.Fprintf(w, "%s: %s / %s / %s\n", mode, target.Organization, target.Project, plan.TargetGroup.Name)

	table := tabwriter.NewWriter(w, 0, 4, 2, ' ', 0)
	fmt.Fprintln(table, "ASSESSMENT\tSOURCE GROUP\tANSWERS\tQUESTIONS\tWORK\tWORK ITEMS\tSTATUS")
	for _, result := range results {
		sourceGroup := result.SourceGroup
		if sourceGroup == "" {
			sourceGroup = "-"
		}
		answers := "-"
		if result.SourceGroup != "" {
			answers = fmt.Sprintf("%d", result.CopiedAnswers)
			if result.DroppedAnswers > 0 {
				answers += fmt.Sprintf(" (%d dropped)", result.DroppedAnswers)
			}
		}
		status := result.Status
		if result.Err != nil {
			status += ": " + result.Err.Error()
		}
		fmt.Fprintf(table, "%s\t%s\t%s\t%d\t%d\t%d\t%s\n",
			result.AssessmentName,
			sourceGroup,
			answers,
			result.QuestionCount,
			result.WorkCount,
			result.WorkItemCount,
			status,
		)
	}
	_ = table.Flush()
	if !options.Apply {
		fmt.Fprintf(w, "\nNo changes made. Review the rows, then rerun with -assessment-group %q -apply.\n", plan.TargetGroup.Name)
	}
}
