// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log"
	"net/http"
	"strconv"
	"strings"

	"github.com/microsoft/azure-devops-go-api/azuredevops/build"
	"github.com/microsoft/go-infra/azdo"
	"github.com/microsoft/go-infra/subcmd"
)

func init() {
	subcommands = append(subcommands, subcmd.Option{
		Name:    "build-pipeline",
		Summary: "Queue an AzDO build pipeline.",
		Description: `

Takes extra args: parameters and variables to queue the build with. Pass a parameter by passing
three args 'p <name> <value>', or pass a variable with 'v <name> <value>'.
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

	var parameters = make(map[string]string)
	var variables = make(map[string]string)

	if url := getEnvBuildURL(); url != "" {
		variables["DebugGoReleaseQueuePipelineOriginURL"] = getEnvBuildURL()
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
	client := new(http.Client)

	variablesJSON, err := json.Marshal(variables)
	if err != nil {
		return err
	}

	url := *azdoFlags.Org + *azdoFlags.Proj + "/_apis/build/builds?definitionId=" + *id + "&api-version=7.1-preview.7"
	body := map[string]interface{}{
		"definition": map[string]interface{}{
			"id": *id,
		},
		"sourceBranch":       *branch,
		"sourceVersion":      *commit,
		"templateParameters": parameters,
		// Variables is a JSON string of a map[string]string. "parameters" is a legacy name in the
		// AzDO UI--this is unrelated to the template parameters.
		"parameters": string(variablesJSON),
	}

	bodyJSON, err := json.MarshalIndent(body, "", "  ")
	if err != nil {
		return err
	}

	req, err := http.NewRequestWithContext(ctx, "POST", url, strings.NewReader(string(bodyJSON)))
	if err != nil {
		return err
	}

	log.Printf("Sending body:\n%v\n", string(bodyJSON))

	// Based on https://github.com/microsoft/azure-devops-go-api/blob/00dac5c867394a3c5ca4e12b6965d7625a1588c6/azuredevops/client.go#L172-L181
	req.Header.Add("Authorization", azdoFlags.NewConnection().AuthorizationString)
	req.Header.Add("Accept", "application/json;api-version=7.1-preview.7")
	req.Header.Add("Content-Type", "application/json;charset=utf-8")

	resp, err := client.Do(req)
	if err != nil {
		return err
	}
        defer resp.Body.Close()
	bodyBytes, err := io.ReadAll(resp.Body)
	if err != nil {
		return err
	}
	if resp != nil && (resp.StatusCode < 200 || resp.StatusCode >= 300) {
		return fmt.Errorf("non-success status code: %#v\nresponse data: %v", resp, string(bodyBytes))
	}

	var b build.Build
	if err := json.Unmarshal(bodyBytes, &b); err != nil {
		return err
	}

	log.Printf("Queued build id %v\n", *b.Id)
	if *setVariable != "" {
		azdo.SetPipelineVariable(*setVariable, strconv.Itoa(*b.Id))
	}

	if url, ok := azdo.GetBuildWebURL(&b); ok {
		log.Printf("Web build URL: %v\n", url)
	} else {
		log.Printf("Unable to find web URL in API response: %v\n", b.Links)
	}

	return nil
}
