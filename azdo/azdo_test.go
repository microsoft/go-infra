// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package azdo

import (
	"encoding/json"
	"net/http"
	"testing"

	"github.com/microsoft/azure-devops-go-api/azuredevops/v7"
	"github.com/microsoft/azure-devops-go-api/azuredevops/v7/build"
)

func TestClientFlagsV7Connection(t *testing.T) {
	flags := ClientFlags{Org: new("https://dev.azure.com/example/"), Proj: new("project"), PAT: new("test-only-pat")}
	var connection *azuredevops.Connection = flags.NewConnection()
	client := azuredevops.NewClientWithOptions(connection, connection.BaseUrl, azuredevops.WithHTTPClient(&http.Client{}))
	req, err := client.CreateRequestMessage(t.Context(), http.MethodGet, connection.BaseUrl+"/_apis", "7.1", nil, "", "application/json", nil)
	if err != nil {
		t.Fatal(err)
	}
	_, password, ok := req.BasicAuth()
	if !ok || password != *flags.PAT {
		t.Error("connection did not configure PAT authentication")
	}
	if req.Header.Get("X-TFS-FedAuthRedirect") != "Suppress" {
		t.Error("connection did not suppress sign-in redirects")
	}
}

func TestGetBuildWebURLV7(t *testing.T) {
	for _, tc := range []struct {
		body string
		want string
	}{
		{`{"id":123,"_links":{"web":{"href":"https://dev.azure.com/example/project/_build/results?buildId=123"}}}`, "https://dev.azure.com/example/project/_build/results?buildId=123"},
		{`{"id":123}`, ""},
		{`{"id":123,"_links":{"self":{"href":"https://dev.azure.com/example/_apis/build/builds/123"}}}`, ""},
		{`{"id":123,"_links":{"web":{"href":123}}}`, ""},
	} {
		var b build.Build
		if err := json.Unmarshal([]byte(tc.body), &b); err != nil {
			t.Fatal(err)
		}
		url, ok := GetBuildWebURL(&b)
		if url != tc.want || ok != (tc.want != "") {
			t.Errorf("GetBuildWebURL() = %q, %v; want %q", url, ok, tc.want)
		}
	}
}
