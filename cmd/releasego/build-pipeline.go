// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"log"
	"net/http"
	"strconv"
	"time"

	"github.com/microsoft/azure-devops-go-api/azuredevops/v7"
	"github.com/microsoft/azure-devops-go-api/azuredevops/v7/build"
	"github.com/microsoft/go-infra/azdo"
	"github.com/microsoft/go-infra/stringutil"
	"github.com/microsoft/go-infra/subcmd"
)

func init() {
	subcommands = append(subcommands, subcmd.Option{
		Name:    "build-pipeline",
		Summary: "Queue an AzDO build pipeline.",
		Description: `

Takes extra args defining the parameters and variables to queue the build with:

  p <name> <value>
    Pass a parameter. The parameter must be accepted by the target pipeline or
    this command fails.

  pOptional <name> <value>
    Pass a parameter, but if the target pipeline doesn't accept it, try again
    without this parameter. This may be useful for backward compatibility.

  v <name> <value>
    Pass a variable. AzDO pipelines don't validate variables before running.
`,
		TakeArgsReason: "Parameters and variables to pass to the build.",
		Handle:         handleBuildPipeline,
	})
}

func handleBuildPipeline(p subcmd.ParseFunc) error {
	id := flag.String("id", "", "[Required] The decimal ID of the AzDO pipeline to queue.")
	commit := flag.String("commit", "", "A specific commit to build.")
	branch := flag.String("branch", "", "The branch that contains commit. Only necessary if the repo's default branch doesn't contain commit.")
	setVariable := flag.String("set-azdo-variable", "", "An AzDO variable name to set to the ID of the queued build.")
	azdoFlags := azdo.BindClientFlags()

	if err := p(); err != nil {
		return err
	}

	// Pipeline IDs are decimal, including when supplied with leading zeros.
	// flag.Int uses base 0 and would interpret those IDs as octal.
	definitionID, err := strconv.Atoi(*id)
	if err != nil || definitionID <= 0 {
		flag.Usage()
		return errors.New("a positive decimal pipeline ID is required")
	}
	if err := azdoFlags.EnsureAssigned(); err != nil {
		flag.Usage()
		return err
	}

	// parameters contains both optional and non-optional parameters.
	parameters := make(map[string]string)
	optionalParameters := make(map[string]string)
	variables := make(map[string]string)

	if url := azdo.GetEnvBuildURL(); url != "" {
		variables["DebugGoReleaseQueuePipelineOriginURL"] = url
	}

	for i := 0; i < len(flag.Args()); i++ {
		a := flag.Args()[i]
		remaining := flag.Args()[i:]
		switch a {
		case "p":
			if len(remaining) < 3 {
				return fmt.Errorf("not enough args remaining for 'p': %v", remaining)
			}
			parameters[remaining[1]] = remaining[2]
			i += 2

		case "pOptional":
			if len(remaining) < 3 {
				return fmt.Errorf("not enough args remaining for 'pOptional': %v", remaining)
			}
			parameters[remaining[1]] = remaining[2]
			optionalParameters[remaining[1]] = remaining[2]
			i += 2

		case "v":
			if len(remaining) < 3 {
				return fmt.Errorf("not enough args remaining for 'v': %v", remaining)
			}
			variables[remaining[1]] = remaining[2]
			i += 2

		default:
			flag.Usage()
			log.Fatalf("Unrecognized arg: %v\n", a)
		}
	}

	ctx := context.Background()

	connection := azdoFlags.NewConnection()
	// Generous timeout. Maximum observed time on dev machine during development: 10 seconds.
	connection.Timeout = new(time.Minute * 3)
	client, err := build.NewClient(ctx, connection)
	if err != nil {
		return err
	}

	request := &buildPipelineRequest{
		DefinitionID:  definitionID,
		SourceBranch:  *branch,
		SourceVersion: *commit,
		Parameters:    parameters,
		Variables:     variables,
	}

	b, err := sendBuildPipelineRunRequest(ctx, client, *azdoFlags.Proj, request)
	if err != nil {
		var reqErr *errBuildPipelineBadRequest
		if !errors.As(err, &reqErr) {
			return err
		}
		// Retry. AzDO should send the full list of unexpected parameters back to us in the 400
		// response (if any), so only a single retry is needed.

		// If there are no unexpected parameters (something else went wrong),
		// there's no point in retrying.
		if len(reqErr.unexpectedParameters) == 0 {
			return err
		}

		// Check that all unexpected parameters are optional, and if so, get ready to run the
		// request again with each one removed from the map of parameters.
		var nonOptional []string
		for _, unexpected := range reqErr.unexpectedParameters {
			if _, ok := optionalParameters[unexpected]; ok {
				delete(parameters, unexpected)
			} else {
				nonOptional = append(nonOptional, unexpected)
			}
		}
		if len(nonOptional) > 0 {
			return fmt.Errorf("response indicated unexpected parameters %q, which are not optional", nonOptional)
		}

		log.Printf("Retrying after removing unexpected parameters %q\n", reqErr.unexpectedParameters)
		b, err = sendBuildPipelineRunRequest(ctx, client, *azdoFlags.Proj, request)
		if err != nil {
			return fmt.Errorf("failed retry after removing unexpected parameters: %w", err)
		}
	}

	log.Printf("Queued build id %v\n", *b.Id)
	if *setVariable != "" {
		azdo.LogCmdSetVariable(*setVariable, strconv.Itoa(*b.Id))
	}

	if url, ok := azdo.GetBuildWebURL(b); ok {
		log.Printf("Web build URL: %v\n", url)
	} else {
		log.Printf("Unable to find web URL in API response: %v\n", b.Links)
	}

	return nil
}

type buildPipelineRequest struct {
	DefinitionID  int
	SourceBranch  string
	SourceVersion string
	Parameters    map[string]string
	Variables     map[string]string
}

type errBuildPipelineBadRequest struct {
	unexpectedParameters []string
	cause                error
}

func (e *errBuildPipelineBadRequest) Error() string {
	if len(e.unexpectedParameters) == 0 {
		return fmt.Sprintf("build pipeline request got 400 response: %v", e.cause)
	}
	return fmt.Sprintf("build pipeline request got 400 response; unexpected parameters: %#v", e.unexpectedParameters)
}

func (e *errBuildPipelineBadRequest) Unwrap() error { return e.cause }

func sendBuildPipelineRunRequest(ctx context.Context, client build.Client, project string, request *buildPipelineRequest) (*build.Build, error) {
	variablesJSON, err := json.Marshal(request.Variables)
	if err != nil {
		return nil, err
	}
	log.Printf("Queuing pipeline %d...", request.DefinitionID)
	b, err := client.QueueBuild(ctx, build.QueueBuildArgs{
		Project: &project,
		Build: &build.Build{
			Definition:         &build.DefinitionReference{Id: &request.DefinitionID},
			SourceBranch:       &request.SourceBranch,
			SourceVersion:      &request.SourceVersion,
			TemplateParameters: &request.Parameters,
			// The Build API's legacy "parameters" field carries variables as
			// JSON text, independently of YAML template parameters.
			Parameters: new(string(variablesJSON)),
		},
	})
	if err != nil {
		return nil, buildPipelineRequestError(err)
	}
	if b == nil || b.Id == nil || *b.Id <= 0 {
		return nil, errors.New("queue build response did not contain a valid build ID")
	}
	return b, nil
}

func buildPipelineRequestError(err error) error {
	// The SDK returns both value and pointer WrappedErrors, depending on the
	// response shape. Preserve the original error for non-validation failures.
	wrapped, ok := errors.AsType[azuredevops.WrappedError](err)
	if !ok {
		pointer, found := errors.AsType[*azuredevops.WrappedError](err)
		if !found || pointer == nil {
			return err
		}
		wrapped = *pointer
	}
	if wrapped.StatusCode == nil || *wrapped.StatusCode != http.StatusBadRequest {
		return err
	}
	reqErr := &errBuildPipelineBadRequest{cause: err}
	if wrapped.CustomProperties == nil {
		return reqErr
	}
	var properties struct {
		ValidationResults []build.BuildRequestValidationResult `json:"validationResults"`
	}
	data, marshalErr := json.Marshal(wrapped.CustomProperties)
	if marshalErr != nil || json.Unmarshal(data, &properties) != nil {
		return err
	}
	for _, vr := range properties.ValidationResults {
		if vr.Result == nil || *vr.Result != build.ValidationResultValues.Error || vr.Message == nil {
			continue
		}
		before, name, after, found := stringutil.CutTwice(*vr.Message, "Unexpected parameter '", "'")
		if found && before == "" && after == "" {
			reqErr.unexpectedParameters = append(reqErr.unexpectedParameters, name)
		}
	}
	return reqErr
}
