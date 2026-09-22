// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"net/http/httptest"
	"os"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/microsoft/azure-devops-go-api/azuredevops/v7"
	"github.com/microsoft/azure-devops-go-api/azuredevops/v7/build"
)

func TestBuildPipelineSDKRequest(t *testing.T) {
	t.Setenv("SYSTEM_COLLECTIONURI", "")
	t.Setenv("SYSTEM_TEAMPROJECT", "")
	t.Setenv("BUILD_BUILDID", "")
	var logs bytes.Buffer
	oldLog := log.Writer()
	log.SetOutput(&logs)
	defer log.SetOutput(oldLog)
	var queued atomic.Bool
	s := newAzureSDKServer(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/collection/My Project/_apis/build/builds" {
			t.Errorf("unexpected queue request: %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		var body struct {
			Definition                              struct{ ID int }
			SourceBranch, SourceVersion, Parameters string
			TemplateParameters                      map[string]string
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
		}
		if body.Definition.ID != 191 || body.SourceBranch != "refs/heads/main" || body.SourceVersion != "abc123" {
			t.Errorf("wrong queued build identity: %+v", body)
		}
		if !reflect.DeepEqual(body.TemplateParameters, map[string]string{"version": "1.26.1", "newFeature": "true"}) {
			t.Errorf("template parameters were not sent: %v", body.TemplateParameters)
		}
		var variables map[string]string
		if json.Unmarshal([]byte(body.Parameters), &variables) != nil || !reflect.DeepEqual(variables, map[string]string{"SecretVariable": "variable-value-not-for-logs"}) {
			t.Error("variables did not use the Build API's legacy parameters field")
		}
		queued.Store(true)
		writeAzureSDKResponse(t, w, `{"id":123,"_links":{"web":{"href":"https://dev.azure.com/example/project/_build/results?buildId=123"}}}`)
	})
	out, err := captureCommandOutput(t, func() error {
		return runAzureCommand(t, handleBuildPipeline, azureCommandArgs(s,
			"-id=191", "-commit=abc123", "-branch=refs/heads/main", "-set-azdo-variable=QueuedBuild",
			"p", "version", "1.26.1", "pOptional", "newFeature", "true", "v", "SecretVariable", "variable-value-not-for-logs")...)
	})
	if err != nil {
		t.Fatal(err)
	}
	if !queued.Load() || !strings.Contains(out, "##vso[task.setvariable variable=QueuedBuild]123") {
		t.Fatalf("build was not queued/reported: %q", out)
	}
	if strings.Contains(logs.String(), sdkTestPAT) || strings.Contains(logs.String(), "variable-value-not-for-logs") {
		t.Fatal("queueing logs exposed credentials or variable values")
	}
}

func TestBuildPipelineSDKValidationRetry(t *testing.T) {
	for _, tc := range []struct {
		name       string
		status     int
		body       string
		secondFail bool
		wantCalls  int
		wantError  bool
	}{
		{"optional", 400, `{"message":"invalid parameters","customProperties":{"ValidationResults":[{"result":"error","message":"Unexpected parameter 'newFeature'"}]}}`, false, 2, false},
		{"lowercase properties", 400, `{"message":"invalid parameters","customProperties":{"validationResults":[{"result":"error","message":"Unexpected parameter 'newFeature'"}]}}`, false, 2, false},
		{"required", 400, `{"message":"invalid parameters","customProperties":{"ValidationResults":[{"result":"error","message":"Unexpected parameter 'version'"}]}}`, false, 1, true},
		{"mixed", 400, `{"message":"invalid parameters","customProperties":{"ValidationResults":[{"result":"error","message":"Unexpected parameter 'newFeature'"},{"result":"error","message":"Unexpected parameter 'version'"}]}}`, false, 1, true},
		{"unrelated validation", 400, `{"message":"invalid branch","customProperties":{"ValidationResults":[{"result":"error","message":"Branch does not exist"}]}}`, false, 1, true},
		{"warning", 400, `{"message":"invalid build","customProperties":{"ValidationResults":[{"result":"warning","message":"Unexpected parameter 'newFeature'"}]}}`, false, 1, true},
		{"malformed properties", 400, `{"message":"invalid build","customProperties":{"ValidationResults":"not an array"}}`, false, 1, true},
		{"empty response", 400, "", false, 1, true},
		{"forbidden", 403, `{"message":"forbidden"}`, false, 1, true},
		{"server error", 500, `{"message":"unavailable"}`, false, 1, true},
		{"invalid JSON", 200, "<html>sign in</html>", false, 1, true},
		{"missing build ID", 200, `{}`, false, 1, true},
		{"second failure", 400, `{"message":"invalid parameters","customProperties":{"ValidationResults":[{"result":"error","message":"Unexpected parameter 'newFeature'"}]}}`, true, 2, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("SYSTEM_COLLECTIONURI", "")
			t.Setenv("SYSTEM_TEAMPROJECT", "")
			t.Setenv("BUILD_BUILDID", "")
			var calls atomic.Int32
			s := newAzureSDKServer(t, func(w http.ResponseWriter, r *http.Request) {
				if r.Method != http.MethodPost || r.URL.Path != "/collection/My Project/_apis/build/builds" {
					t.Errorf("unexpected queue request: %s %s", r.Method, r.URL.Path)
					w.WriteHeader(http.StatusBadRequest)
					return
				}
				attempt := calls.Add(1)
				var body struct {
					TemplateParameters map[string]string
					Parameters         string
				}
				if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
					t.Error(err)
				}
				want := map[string]string{"version": "1.26.1", "newFeature": "true"}
				if attempt > 1 {
					delete(want, "newFeature")
				}
				if !reflect.DeepEqual(body.TemplateParameters, want) {
					t.Errorf("attempt %d template parameters = %v, want %v", attempt, body.TemplateParameters, want)
				}
				var variables map[string]string
				if json.Unmarshal([]byte(body.Parameters), &variables) != nil || !reflect.DeepEqual(variables, map[string]string{"buildVar": "preserved"}) {
					t.Error("retry changed build variables")
				}
				if attempt == 1 || tc.secondFail {
					w.WriteHeader(tc.status)
					writeAzureSDKResponse(t, w, tc.body)
				} else {
					writeAzureSDKResponse(t, w, `{"id":123}`)
				}
			})
			err := runAzureCommand(t, handleBuildPipeline, azureCommandArgs(s, "-id=191", "p", "version", "1.26.1", "pOptional", "newFeature", "true", "v", "buildVar", "preserved")...)
			if (err != nil) != tc.wantError || int(calls.Load()) != tc.wantCalls {
				t.Fatalf("got error %v, %d requests; want error=%v, %d requests", err, calls.Load(), tc.wantError, tc.wantCalls)
			}
			if tc.secondFail {
				wrapped, ok := errors.AsType[azuredevops.WrappedError](err)
				if !ok || wrapped.StatusCode == nil || *wrapped.StatusCode != tc.status {
					t.Error("retry error lost its underlying SDK status")
				}
			}
		})
	}
}

func TestBuildPipelineWrappedSDKErrors(t *testing.T) {
	wrapped := azuredevops.WrappedError{
		StatusCode: new(http.StatusBadRequest), Message: new("invalid parameters"),
		CustomProperties: &map[string]any{"ValidationResults": []any{map[string]any{"result": "error", "message": "Unexpected parameter 'optional'"}}},
	}
	for _, original := range []error{wrapped, &wrapped, fmt.Errorf("wrapped: %w", wrapped), fmt.Errorf("wrapped: %w", &wrapped)} {
		got := buildPipelineRequestError(original)
		if !strings.Contains(got.Error(), "unexpected parameters") || !strings.Contains(got.Error(), "optional") || !errors.Is(got, original) {
			t.Errorf("validation error was not recognized/preserved for %T: %v", original, got)
		}
	}
	for _, original := range []error{errors.New("transport failed"), azuredevops.WrappedError{StatusCode: new(403), Message: new("forbidden")}} {
		if got := buildPipelineRequestError(original); got != original {
			t.Errorf("non-validation error was changed: %v", got)
		}
	}
}

func TestBuildPipelineRejectsInvalidDefinitionID(t *testing.T) {
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Error("invalid definition ID must be rejected before contacting Azure")
		w.WriteHeader(http.StatusBadRequest)
	}))
	defer s.Close()
	for _, id := range []string{"", "0", "-1", "not-an-id", "0xBF", "999999999999999999999999"} {
		err := runAzureCommand(t, handleBuildPipeline, azureCommandArgs(s, "-id="+id)...)
		if err == nil || !strings.Contains(err.Error(), "positive decimal pipeline ID") {
			t.Errorf("invalid definition ID %q: want ID validation error, got %v", id, err)
		}
	}
}

func TestBuildPipelineDecimalDefinitionID(t *testing.T) {
	for _, tc := range []struct {
		id   string
		want int
	}{{"191", 191}, {"0191", 191}, {"0100", 100}} {
		t.Run(tc.id, func(t *testing.T) {
			t.Setenv("SYSTEM_COLLECTIONURI", "")
			t.Setenv("SYSTEM_TEAMPROJECT", "")
			t.Setenv("BUILD_BUILDID", "")
			var queued atomic.Bool
			s := newAzureSDKServer(t, func(w http.ResponseWriter, r *http.Request) {
				if r.Method != http.MethodPost || r.URL.Path != "/collection/My Project/_apis/build/builds" {
					t.Errorf("unexpected queue request: %s %s", r.Method, r.URL.Path)
					w.WriteHeader(http.StatusBadRequest)
					return
				}
				var body struct{ Definition struct{ ID int } }
				if json.NewDecoder(r.Body).Decode(&body) != nil || body.Definition.ID != tc.want {
					t.Errorf("queued definition %d, want %d", body.Definition.ID, tc.want)
				}
				queued.Store(true)
				writeAzureSDKResponse(t, w, `{"id":123}`)
			})
			if err := runAzureCommand(t, handleBuildPipeline, azureCommandArgs(s, "-id="+tc.id)...); err != nil {
				t.Fatal(err)
			}
			if !queued.Load() {
				t.Fatal("build was not queued")
			}
		})
	}
}

func TestBuildPipelineSDKResourceAreaRouting(t *testing.T) {
	t.Setenv("SYSTEM_COLLECTIONURI", "")
	t.Setenv("SYSTEM_TEAMPROJECT", "")
	t.Setenv("BUILD_BUILDID", "")
	var queued atomic.Bool
	area := newAzureSDKServer(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/collection/My Project/_apis/build/builds" {
			t.Errorf("unexpected resource-area request: %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		queued.Store(true)
		writeAzureSDKResponse(t, w, `{"id":123}`)
	})
	locations, err := os.ReadFile("testdata/azure-sdk-locations.json")
	if err != nil {
		t.Fatal(err)
	}
	org := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, password, ok := r.BasicAuth()
		if !ok || password != sdkTestPAT {
			t.Error("missing authentication during resource discovery")
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodOptions && r.URL.Path == "/collection/_apis":
			writeAzureSDKResponse(t, w, string(locations))
		case r.Method == http.MethodGet && r.URL.Path == "/collection/_apis/resourceAreas":
			areas := []azuredevops.ResourceAreaInfo{{Id: &build.ResourceAreaId, LocationUrl: new(area.URL + "/collection")}}
			if err := json.NewEncoder(w).Encode(map[string]any{"count": len(areas), "value": areas}); err != nil {
				t.Error(err)
			}
		default:
			t.Errorf("build request was not routed to the discovered resource area: %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusBadRequest)
		}
	}))
	defer org.Close()
	if err := runAzureCommand(t, handleBuildPipeline, azureCommandArgs(org, "-id=191")...); err != nil {
		t.Fatal(err)
	}
	if !queued.Load() {
		t.Fatal("build was not queued at the discovered resource area")
	}
}

func TestBuildPipelineSDKDiscoveryErrors(t *testing.T) {
	locations, err := os.ReadFile("testdata/azure-sdk-locations.json")
	if err != nil {
		t.Fatal(err)
	}
	for _, stage := range []string{"locations", "resource areas"} {
		for _, response := range []struct {
			status int
			body   string
		}{
			{http.StatusUnauthorized, `{"message":"unauthorized"}`},
			{http.StatusForbidden, `{"message":"forbidden"}`},
			{http.StatusServiceUnavailable, `{"message":"unavailable"}`},
			{http.StatusOK, `<html>sign in</html>`},
		} {
			t.Run(fmt.Sprint(stage, "/", response.status), func(t *testing.T) {
				t.Setenv("SYSTEM_COLLECTIONURI", "")
				t.Setenv("SYSTEM_TEAMPROJECT", "")
				t.Setenv("BUILD_BUILDID", "")
				s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					w.Header().Set("Content-Type", "application/json")
					if stage == "resource areas" && r.Method == http.MethodOptions && r.URL.Path == "/collection/_apis" {
						writeAzureSDKResponse(t, w, string(locations))
						return
					}
					if r.Method != http.MethodOptions && (r.Method != http.MethodGet || r.URL.Path != "/collection/_apis/resourceAreas") {
						t.Errorf("queueing proceeded after discovery failed: %s %s", r.Method, r.URL.Path)
					}
					w.WriteHeader(response.status)
					writeAzureSDKResponse(t, w, response.body)
				}))
				defer s.Close()
				if err := runAzureCommand(t, handleBuildPipeline, azureCommandArgs(s, "-id=191")...); err == nil {
					t.Fatal("API discovery error was ignored")
				}
			})
		}
	}
}
