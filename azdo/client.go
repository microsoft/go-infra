// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package azdo

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/google/uuid"
	"github.com/microsoft/azure-devops-go-api/azuredevops/v7"
	"github.com/microsoft/azure-devops-go-api/azuredevops/v7/build"
	"github.com/microsoft/azure-devops-go-api/azuredevops/v7/git"
	"github.com/microsoft/azure-devops-go-api/azuredevops/v7/pipelines"
)

// NewBuildClient creates a build client that reports non-JSON responses.
func NewBuildClient(ctx context.Context, connection *azuredevops.Connection) (build.Client, error) {
	client, err := newResourceAreaClient(ctx, connection, build.ResourceAreaId)
	if err != nil {
		return nil, err
	}
	return &build.ClientImpl{Client: *client}, nil
}

// NewGitClient creates a Git client that reports non-JSON responses.
func NewGitClient(ctx context.Context, connection *azuredevops.Connection) (git.Client, error) {
	client, err := newResourceAreaClient(ctx, connection, git.ResourceAreaId)
	if err != nil {
		return nil, err
	}
	return &git.ClientImpl{Client: *client}, nil
}

// NewPipelinesClient creates a pipelines client that reports non-JSON responses.
func NewPipelinesClient(connection *azuredevops.Connection) pipelines.Client {
	client := newClientForURL(connection, connection.BaseUrl, newHTTPClient(connection))
	return &pipelines.ClientImpl{Client: *client}
}

// newResourceAreaClient replicates the SDK's Connection.GetClientByResourceAreaId
// using an HTTP client that wraps responses with jsonResponseTransport. The SDK
// builds its clients with azuredevops.NewClient, which always constructs its
// own http.Client and offers no way to supply one, so discovery requests would
// otherwise bypass the transport. This can be removed if the SDK lets callers
// provide a custom HTTP client to the connection.
func newResourceAreaClient(ctx context.Context, connection *azuredevops.Connection, areaID uuid.UUID) (*azuredevops.Client, error) {
	httpClient := newHTTPClient(connection)
	baseClient := newClientForURL(connection, connection.BaseUrl, httpClient)
	areas, err := baseClient.GetResourceAreas(ctx)
	if err != nil {
		return nil, err
	}
	if areas == nil {
		return nil, errors.New("resource area response is nil")
	}
	if len(*areas) == 0 {
		return baseClient, nil
	}
	for _, area := range *areas {
		if area.Id == nil {
			return nil, errors.New("resource area has no ID")
		}
		if *area.Id == areaID {
			if area.LocationUrl == nil || *area.LocationUrl == "" {
				return nil, fmt.Errorf("resource area %s has no location URL", areaID)
			}
			return newClientForURL(connection, *area.LocationUrl, httpClient), nil
		}
	}
	return nil, &azuredevops.ResourceAreaIdNotRegisteredError{ResourceAreaId: areaID, Url: connection.BaseUrl}
}

func newClientForURL(connection *azuredevops.Connection, url string, httpClient *http.Client) *azuredevops.Client {
	client := connection.GetClientByUrl(url)
	azuredevops.WithHTTPClient(httpClient)(client)
	return client
}

func newHTTPClient(connection *azuredevops.Connection) *http.Client {
	transport := http.DefaultTransport
	if connection.TlsConfig != nil {
		transport = &http.Transport{TLSClientConfig: connection.TlsConfig}
	}
	client := &http.Client{Transport: &jsonResponseTransport{base: transport}}
	if connection.Timeout != nil {
		client.Timeout = *connection.Timeout
	}
	return client
}

// jsonResponseTransport works around the SDK discarding response bodies that
// it can't decode. As of azure-devops-go-api v7.1.0, Client.UnmarshalBody and
// Client.UnwrapError return only the json.Unmarshal error, so an HTML outage or
// sign-in page surfaces as "invalid character '<' looking for beginning of
// value". Wrapping the body makes the SDK's read fail with a
// NonJSONResponseError that carries the complete response instead. This can be
// removed if the SDK preserves response details in its decode errors.
type jsonResponseTransport struct {
	base http.RoundTripper
}

func (t *jsonResponseTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	resp, err := t.base.RoundTrip(req)
	if err != nil || resp == nil || resp.Body == nil ||
		req.Method == http.MethodHead ||
		!strings.Contains(req.Header.Get("Accept"), "application/json") {

		return resp, err
	}

	resp.Body = &jsonResponseBody{
		ReadCloser:  resp.Body,
		status:      resp.Status,
		statusCode:  resp.StatusCode,
		contentType: resp.Header.Get("Content-Type"),
	}
	return resp, nil
}

type jsonResponseBody struct {
	io.ReadCloser
	body        bytes.Buffer
	status      string
	statusCode  int
	contentType string
}

func (r *jsonResponseBody) Read(p []byte) (int, error) {
	n, err := r.ReadCloser.Read(p)
	r.body.Write(p[:n])
	if err != io.EOF {
		return n, err
	}
	body := r.body.Bytes()
	jsonBody := bytes.TrimPrefix(body, []byte{0xef, 0xbb, 0xbf})
	if !json.Valid(jsonBody) {
		var value json.RawMessage
		jsonErr := json.Unmarshal(jsonBody, &value)
		return n, &NonJSONResponseError{
			Status:      r.status,
			StatusCode:  r.statusCode,
			ContentType: r.contentType,
			Body:        string(body),
			Err:         jsonErr,
		}
	}
	return n, err
}

// NonJSONResponseError includes the complete response that could not be decoded as JSON.
type NonJSONResponseError struct {
	Status      string
	StatusCode  int
	ContentType string
	Body        string
	Err         error
}

func (e *NonJSONResponseError) Error() string {
	message := fmt.Sprintf("Azure DevOps returned a non-JSON response (HTTP %s, content-type %q, body %q): %v",
		e.Status, e.ContentType, e.Body, e.Err)
	// Azure Pipelines interprets logging commands anywhere in an output line.
	return strings.NewReplacer("##vso[", `\x23#vso[`, "##[", `\x23#[`).Replace(message)
}

func (e *NonJSONResponseError) Unwrap() error { return e.Err }
