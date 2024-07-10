// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"net/http"
	"strconv"
	"time"

	"github.com/microsoft/azure-devops-go-api/azuredevops/build"
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
    Pass a variable. A variable is a simple bag and has no validation.
`,
		TakeArgsReason: "Parameters and variables to pass to the build.",
		Handle:         handleBuildPipeline,
	})
}

func handleBuildPipeline(p subcmd.ParseFunc) error {
	id := flag.String("id", "", "[Required] The ID of the AzDO pipeline to queue.")
	commit := flag.String("commit", "", "A specific commit to build.")
	branch := flag.String("branch", "", "The branch that contains commit. Only necessary if the repo's default branch doesn't contain commit.")
	setVariable := flag.String("set-azdo-variable", "", "An AzDO variable name to set to the ID of the queued build.")
	azdoFlags := azdo.BindClientFlags()

	if err := p(); err != nil {
		return err
	}

	if *id == "" {
		flag.Usage()
		log.Fatalln("No pipeline ID specified.")
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

	// Make our own client. The AzDO library doesn't support the 7.1 API needed to pass parameters:
	// https://docs.microsoft.com/en-us/rest/api/azure/devops/build/builds/queue?view=azure-devops-rest-7.1
	client := http.Client{
		// Generous timeout. Maximum observed time on dev machine during development: 10 seconds.
		Timeout: time.Minute * 3,
	}

	request := &buildPipelineRequest{
		DefinitionID:  *id,
		SourceBranch:  *branch,
		SourceVersion: *commit,
		Parameters:    parameters,
		Variables:     variables,
	}

	b, err := sendBuildPipelineRunRequest(ctx, &client, azdoFlags, request)
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
		b, err = sendBuildPipelineRunRequest(ctx, &client, azdoFlags, request)
		if err != nil {
			return fmt.Errorf("failed retry after removing unexpected parameters: %v", err)
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
	DefinitionID  string
	SourceBranch  string
	SourceVersion string
	Parameters    map[string]string
	Variables     map[string]string
}

type errBuildPipelineBadRequest struct {
	unexpectedParameters []string
}

func (e *errBuildPipelineBadRequest) Error() string {
	return fmt.Sprintf("build pipeline request got 400 response; unexpected parameters: %#v", e.unexpectedParameters)
}

func sendBuildPipelineRunRequest(ctx context.Context, client *http.Client, azdoFlags *azdo.ClientFlags, request *buildPipelineRequest) (*build.Build, error) {
	variablesJSON, err := json.Marshal(request.Variables)
	if err != nil {
		return nil, err
	}
	body := map[string]interface{}{
		"definition": map[string]interface{}{
			"id": request.DefinitionID,
		},
		"sourceBranch":       request.SourceBranch,
		"sourceVersion":      request.SourceVersion,
		"templateParameters": request.Parameters,
		// Variables is a JSON string of a map[string]string. "parameters" is a legacy name in the
		// AzDO UI--this is unrelated to the template parameters.
		"parameters": string(variablesJSON),
	}

	url := *azdoFlags.Org + *azdoFlags.Proj +
		"/_apis/build/builds?definitionId=" +
		request.DefinitionID +
		"&api-version=7.1-preview.7"
	bodyJSON, err := json.MarshalIndent(body, "", "  ")
	if err != nil {
		return nil, err
	}

	log.Printf("Sending body to %q:\n%v\n", url, string(bodyJSON))
	req, err := http.NewRequestWithContext(ctx, "POST", url, bytes.NewReader(bodyJSON))
	if err != nil {
		return nil, err
	}

	// Based on https://github.com/microsoft/azure-devops-go-api/blob/00dac5c867394a3c5ca4e12b6965d7625a1588c6/azuredevops/client.go#L172-L181
	req.Header.Add("Authorization", azdoFlags.NewConnection().AuthorizationString)
	req.Header.Add("Accept", "application/json;api-version=7.1-preview.7")
	req.Header.Add("Content-Type", "application/json;charset=utf-8")

	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	bodyBytes, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode == http.StatusBadRequest {
		// Try to parse the error response for specific types of issues the caller is interested in.
		bodyObj := struct {
			CustomProperties struct {
				ValidationResults []struct {
					Result  string `json:"result"`
					Message string `json:"message"`
				}
			} `json:"customProperties"`
		}{}
		if err := json.Unmarshal(bodyBytes, &bodyObj); err != nil {
			return nil, fmt.Errorf("failed to parse 400 error response: %v", err)
		}
		var reqErr errBuildPipelineBadRequest
		for _, vr := range bodyObj.CustomProperties.ValidationResults {
			if vr.Result != "error" {
				continue
			}
			before, name, after, found := stringutil.CutTwice(vr.Message, "Unexpected parameter '", "'")
			if !found || before != "" || after != "" {
				continue
			}
			reqErr.unexpectedParameters = append(reqErr.unexpectedParameters, name)
		}
		return nil, &reqErr
	}
	if resp != nil && (resp.StatusCode < 200 || resp.StatusCode >= 300) {
		return nil, fmt.Errorf("non-success status code: %#v\nresponse data: %v", resp, string(bodyBytes))
	}

	var b build.Build
	if err := json.Unmarshal(bodyBytes, &b); err != nil {
		return nil, err
	}
	return &b, nil
}
