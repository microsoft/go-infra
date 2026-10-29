// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package azdo

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/microsoft/azure-devops-go-api/azuredevops/v7"
	"github.com/microsoft/azure-devops-go-api/azuredevops/v7/build"
)

func TestEscapeLogCmdData(t *testing.T) {
	for _, tt := range []struct {
		input, want string
	}{
		{"plain text", "plain text"},
		{"line1\r\nline2", "line1%0D%0Aline2"},
		{"%0A %0D %AZP25 100%", "%AZP250A %AZP250D %AZP25AZP25 100%AZP25"},
	} {
		if got := EscapeLogCmdData(tt.input); got != tt.want {
			t.Errorf("EscapeLogCmdData(%q) = %q, want %q", tt.input, got, tt.want)
		}
	}
}

func TestSanitizeBuildNumber(t *testing.T) {
	for _, tt := range []struct {
		name, input, want string
	}{
		{"release", "123_release-branch.go1.42_1.42.1-2", "123_release-branch.go1.42_1.42.1-2"},
		{"official", "123_dev/official/1.42_1.42.1", "123_dev_official_1.42_1.42.1"},
		{"invalid characters", "123_\"'/\\:<>|?@*\r\n_1.42.", "123_" + strings.Repeat("_", 13) + "_1.42"},
		{"length limit", "123_" + strings.Repeat("a", 300) + "_1.42", "123_" + strings.Repeat("a", 251)},
		{"period at length limit", strings.Repeat("a", 254) + ".extra", strings.Repeat("a", 254)},
	} {
		t.Run(tt.name, func(t *testing.T) {
			if got := SanitizeBuildNumber(tt.input); got != tt.want {
				t.Errorf("SanitizeBuildNumber(%q) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}

func TestClientFlagsV7Connection(t *testing.T) {
	flags := ClientFlags{Org: new("https://dev.azure.com/example/"), Proj: new("project"), PAT: new("test-only-pat")}
	connection := flags.NewConnection()
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
