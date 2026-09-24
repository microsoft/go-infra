// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package main

import (
	"context"
	"encoding/base64"
	"testing"
	"time"

	"github.com/chromedp/cdproto/network"
	"github.com/chromedp/chromedp"
)

func TestPortalGrantURL(t *testing.T) {
	got := portalGrantURL(repository{Owner: "microsoft", Name: "go-infra"})
	want := "https://repos.opensource.microsoft.com/orgs/microsoft/repos/go-infra/jit/grant"
	if got != want {
		t.Fatalf("URL = %q, want %q", got, want)
	}
}

func TestIsElevationMutation(t *testing.T) {
	tests := []struct {
		method  string
		url     string
		capture bool
		matched bool
		want    bool
	}{
		{"POST", "https://repos.opensource.microsoft.com/api/client/context/orgs/microsoft/jit", false, false, true},
		{"PUT", "https://repos.opensource.microsoft.com/api/client/orgs/microsoft/repos/go-infra/elevation", false, false, true},
		{"GET", "https://repos.opensource.microsoft.com/api/client/context/orgs/microsoft/jit", true, true, false},
		{"POST", "https://example.com/api/jit", true, true, false},
		{"POST", "https://repos.opensource.microsoft.com/api/client/context", false, true, false},
		{"POST", "https://repos.opensource.microsoft.com/api/client/context", true, false, false},
		{"POST", "https://repos.opensource.microsoft.com/api/client/context", true, true, true},
		{"POST", "https://repos.opensource.microsoft.com/api/client/signout", true, true, false},
	}
	for _, test := range tests {
		if got := isElevationMutation(test.method, test.url, test.capture, test.matched); got != test.want {
			t.Errorf(
				"isElevationMutation(%q, %q, %v, %v) = %v, want %v",
				test.method,
				test.url,
				test.capture,
				test.matched,
				got,
				test.want,
			)
		}
	}
}

func TestRequestDataContains(t *testing.T) {
	description := `Investigate "release" failure`
	for _, value := range []string{
		`{"description":"Investigate \"release\" failure"}`,
		"description=Investigate+%22release%22+failure",
		base64.StdEncoding.EncodeToString([]byte(`{"reason":"Investigate \"release\" failure"}`)),
	} {
		entries := []*network.PostDataEntry{{Bytes: value}}
		if !requestDataContains(entries, description) {
			t.Errorf("requestDataContains(%q) = false", value)
		}
	}
	if requestDataContains([]*network.PostDataEntry{{Bytes: `{"description":"unrelated"}`}}, description) {
		t.Error("requestDataContains matched unrelated data")
	}
}

func TestPortalResponseError(t *testing.T) {
	tests := []struct {
		body string
		want string
	}{
		{`{"ok":true}`, ""},
		{`{"error":"not authorized"}`, "not authorized"},
		{`{"error":{"message":"offline"}}`, "offline"},
		{``, ""},
	}
	for _, test := range tests {
		if got := portalResponseError([]byte(test.body)); got != test.want {
			t.Errorf("portalResponseError(%q) = %q, want %q", test.body, got, test.want)
		}
	}
}

func TestElevationDOMScripts(t *testing.T) {
	executable, err := findBrowserExecutable()
	if err != nil {
		t.Skip(err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	allocatorOptions := append([]chromedp.ExecAllocatorOption{}, chromedp.DefaultExecAllocatorOptions[:]...)
	allocatorOptions = append(allocatorOptions,
		chromedp.ExecPath(executable),
		chromedp.Flag("headless", true),
	)
	allocatorCtx, cancelAllocator := chromedp.NewExecAllocator(ctx, allocatorOptions...)
	defer cancelAllocator()
	browserCtx, cancelBrowser := chromedp.NewContext(allocatorCtx)
	defer cancelBrowser()

	const page = `<!doctype html>
		<main>
		<div id="dialog">
			<form>
			<label for="justification">Justification</label>
			<textarea id="justification" name="justification" aria-label="Justification for the just-in-time request"></textarea>
			</form>
			<button id="submit" disabled onclick="document.body.dataset.submitted = 'yes'">Elevate gadams to Administrator</button>
		</div>
		</main>
		<script>
			document.getElementById("justification").addEventListener("input", () => {
				document.getElementById("submit").disabled = false;
			});
		</script>`
	pageURL := "data:text/html;base64," + base64.StdEncoding.EncodeToString([]byte(page))
	if err := chromedp.Run(browserCtx, chromedp.Navigate(pageURL)); err != nil {
		t.Fatal(err)
	}

	const description = `Investigate "release" failure`
	if err := fillDescription(browserCtx, description); err != nil {
		t.Fatal(err)
	}
	var complete bool
	if err := chromedp.Run(browserCtx, chromedp.Evaluate(submitButtonScript(true), &complete)); err != nil {
		t.Fatal(err)
	}
	if !complete {
		t.Fatal("submit script did not complete")
	}

	var result struct {
		Description string `json:"description"`
		Submitted   string `json:"submitted"`
	}
	if err := chromedp.Run(browserCtx, chromedp.Evaluate(`({
		description: document.getElementById("justification").value,
		submitted: document.body.dataset.submitted || ""
	})`, &result)); err != nil {
		t.Fatal(err)
	}
	if result.Description != description {
		t.Errorf("description = %q", result.Description)
	}
	if result.Submitted != "yes" {
		t.Errorf("submitted = %q", result.Submitted)
	}
}
