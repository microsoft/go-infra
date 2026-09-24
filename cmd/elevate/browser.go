// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package main

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/chromedp/cdproto/network"
	"github.com/chromedp/chromedp"
)

const portalBaseURL = "https://repos.opensource.microsoft.com"

type browserElevator struct {
	executable string
	profileDir string
	output     io.Writer
}

type elevationResponse struct {
	RequestID network.RequestID
	URL       string
	Status    int64
	Err       error
}

func newBrowserElevator(options browserOptions, output io.Writer) *browserElevator {
	return &browserElevator{
		executable: options.Executable,
		profileDir: options.ProfileDir,
		output:     output,
	}
}

func portalGrantURL(target repository) string {
	parsed, err := url.Parse(portalBaseURL)
	if err != nil {
		panic(err)
	}
	parsed.Path = "/orgs/" + target.Owner + "/repos/" + target.Name + "/jit/grant"
	return parsed.String()
}

func (e *browserElevator) elevate(ctx context.Context, request elevationRequest) error {
	executable := e.executable
	if executable == "" {
		var err error
		executable, err = findBrowserExecutable()
		if err != nil {
			return err
		}
	} else {
		info, err := os.Stat(executable)
		if err != nil {
			return fmt.Errorf("inspect -browser executable: %w", err)
		}
		if info.IsDir() {
			return errors.New("-browser must point to an executable, not a directory")
		}
	}

	profileDir := e.profileDir
	if profileDir == "" {
		var err error
		profileDir, err = defaultProfileDirectory()
		if err != nil {
			return err
		}
	}
	if err := os.MkdirAll(profileDir, 0o700); err != nil {
		return fmt.Errorf("create browser profile directory: %w", err)
	}
	if err := os.Chmod(profileDir, 0o700); err != nil {
		return fmt.Errorf("secure browser profile directory: %w", err)
	}

	allocatorOptions := append([]chromedp.ExecAllocatorOption{}, chromedp.DefaultExecAllocatorOptions[:]...)
	allocatorOptions = append(allocatorOptions,
		chromedp.ExecPath(executable),
		chromedp.UserDataDir(profileDir),
		chromedp.Flag("headless", false),
		chromedp.Flag("hide-scrollbars", false),
		chromedp.Flag("mute-audio", false),
		chromedp.NoFirstRun,
		chromedp.NoDefaultBrowserCheck,
	)
	allocatorCtx, cancelAllocator := chromedp.NewExecAllocator(ctx, allocatorOptions...)
	defer cancelAllocator()
	browserCtx, cancelBrowser := chromedp.NewContext(allocatorCtx)
	defer cancelBrowser()

	responses := make(chan elevationResponse, 1)
	var capturePortalMutation atomic.Bool
	listenForElevationResponse(browserCtx, responses, &capturePortalMutation, request.Description)

	targetURL := portalGrantURL(request.Repository)
	fmt.Fprintf(e.output, "Opening %s in %s.\n", targetURL, filepath.Base(executable))
	fmt.Fprintln(e.output, "Complete Microsoft sign-in in the browser if prompted.")
	if err := chromedp.Run(browserCtx, network.Enable(), chromedp.Navigate(targetURL)); err != nil {
		return fmt.Errorf("open portal: %w", err)
	}

	formCtx, cancelForm := context.WithTimeout(browserCtx, 30*time.Second)
	defer cancelForm()
	if err := fillDescription(formCtx, request.Description); err != nil {
		return err
	}
	if err := waitForScript(formCtx, submitButtonScript(false), "find the elevation form submit button"); err != nil {
		return fmt.Errorf("%w; %s", err, portalFormDiagnostics(browserCtx))
	}
	capturePortalMutation.Store(true)
	if err := waitForScript(formCtx, submitButtonScript(true), "submit the elevation form"); err != nil {
		return err
	}

	responseCtx, cancelResponse := context.WithTimeout(browserCtx, 90*time.Second)
	defer cancelResponse()
	select {
	case response := <-responses:
		if response.Err != nil {
			return response.Err
		}
		body, err := readNetworkResponseBody(responseCtx, response.RequestID)
		if err != nil && response.Status != 204 {
			return fmt.Errorf("read portal response: %w", err)
		}
		if response.Status < 200 || response.Status >= 300 {
			message := portalResponseMessage(body)
			if message == "" {
				message = fmt.Sprintf("HTTP %d", response.Status)
			}
			return fmt.Errorf("portal rejected the elevation request: %s", message)
		}
		if message := portalResponseError(body); message != "" {
			return fmt.Errorf("portal rejected the elevation request: %s", message)
		}
		return nil
	case <-responseCtx.Done():
		message := visiblePortalAlert(browserCtx)
		if message != "" {
			return fmt.Errorf("portal did not complete the elevation request: %s", message)
		}
		return errors.New("submitted the form but did not observe a JIT API response; elevation could not be verified")
	}
}

func defaultProfileDirectory() (string, error) {
	cacheDirectory, err := os.UserCacheDir()
	if err != nil {
		return "", fmt.Errorf("determine user cache directory: %w", err)
	}
	return filepath.Join(cacheDirectory, "microsoft-go-infra", commandName, "browser-profile"), nil
}

func findBrowserExecutable() (string, error) {
	var candidates []string
	switch runtime.GOOS {
	case "darwin":
		candidates = []string{
			"/Applications/Google Chrome.app/Contents/MacOS/Google Chrome",
			"/Applications/Microsoft Edge.app/Contents/MacOS/Microsoft Edge",
			"/Applications/Chromium.app/Contents/MacOS/Chromium",
		}
	case "windows":
		for _, root := range []string{
			os.Getenv("PROGRAMFILES"),
			os.Getenv("PROGRAMFILES(X86)"),
			os.Getenv("LOCALAPPDATA"),
		} {
			if root == "" {
				continue
			}
			candidates = append(candidates,
				filepath.Join(root, "Google", "Chrome", "Application", "chrome.exe"),
				filepath.Join(root, "Microsoft", "Edge", "Application", "msedge.exe"),
				filepath.Join(root, "Chromium", "Application", "chrome.exe"),
			)
		}
	default:
		for _, name := range []string{"google-chrome", "microsoft-edge", "chromium", "chromium-browser"} {
			if path, err := exec.LookPath(name); err == nil {
				candidates = append(candidates, path)
			}
		}
	}
	for _, candidate := range candidates {
		if info, err := os.Stat(candidate); err == nil && !info.IsDir() {
			return candidate, nil
		}
	}
	return "", errors.New("Chrome, Edge, or Chromium was not found; install a supported browser or specify -browser")
}

func waitForScript(ctx context.Context, script, action string) error {
	ticker := time.NewTicker(250 * time.Millisecond)
	defer ticker.Stop()
	var lastErr error
	for {
		var complete bool
		err := chromedp.Run(ctx, chromedp.Evaluate(script, &complete))
		if err == nil && complete {
			return nil
		}
		if err != nil {
			lastErr = err
		}
		select {
		case <-ctx.Done():
			if lastErr != nil {
				return fmt.Errorf("%s: %w (last browser error: %v)", action, ctx.Err(), lastErr)
			}
			return fmt.Errorf("%s: %w", action, ctx.Err())
		case <-ticker.C:
		}
	}
}

func listenForElevationResponse(
	ctx context.Context,
	responses chan<- elevationResponse,
	capturePortalMutation *atomic.Bool,
	description string,
) {
	type trackedRequest struct {
		url    string
		status int64
	}
	var mutex sync.Mutex
	tracked := make(map[network.RequestID]trackedRequest)

	send := func(response elevationResponse) {
		select {
		case responses <- response:
		default:
		}
	}
	chromedp.ListenTarget(ctx, func(event any) {
		mutex.Lock()
		defer mutex.Unlock()

		switch event := event.(type) {
		case *network.EventRequestWillBeSent:
			if request, ok := tracked[event.RequestID]; ok && event.RedirectResponse != nil {
				delete(tracked, event.RequestID)
				send(elevationResponse{
					RequestID: event.RequestID,
					URL:       request.url,
					Err: fmt.Errorf(
						"portal redirected the JIT request (HTTP %d) instead of completing it",
						event.RedirectResponse.Status,
					),
				})
				return
			}
			descriptionMatched := requestDataContains(event.Request.PostDataEntries, description)
			if isElevationMutation(
				event.Request.Method,
				event.Request.URL,
				capturePortalMutation.Load(),
				descriptionMatched,
			) {
				tracked[event.RequestID] = trackedRequest{url: event.Request.URL}
			}
		case *network.EventResponseReceived:
			request, ok := tracked[event.RequestID]
			if !ok {
				return
			}
			request.status = int64(event.Response.Status)
			tracked[event.RequestID] = request
		case *network.EventLoadingFinished:
			request, ok := tracked[event.RequestID]
			if !ok {
				return
			}
			delete(tracked, event.RequestID)
			send(elevationResponse{
				RequestID: event.RequestID,
				URL:       request.url,
				Status:    request.status,
			})
		case *network.EventLoadingFailed:
			request, ok := tracked[event.RequestID]
			if !ok {
				return
			}
			delete(tracked, event.RequestID)
			send(elevationResponse{
				RequestID: event.RequestID,
				URL:       request.url,
				Err:       fmt.Errorf("portal JIT request failed to load: %s", event.ErrorText),
			})
		}
	})
}

func isElevationMutation(method, rawURL string, capturePortalMutation, descriptionMatched bool) bool {
	switch strings.ToUpper(method) {
	case "POST", "PUT", "PATCH", "DELETE":
	default:
		return false
	}
	parsed, err := url.Parse(rawURL)
	if err != nil || !strings.EqualFold(parsed.Hostname(), "repos.opensource.microsoft.com") {
		return false
	}
	path := strings.ToLower(parsed.Path)
	if strings.Contains(path, "/jit") || strings.Contains(path, "elevat") {
		return true
	}
	return capturePortalMutation &&
		descriptionMatched &&
		strings.HasPrefix(path, "/api/client/") &&
		!strings.Contains(path, "signout")
}

func requestDataContains(entries []*network.PostDataEntry, description string) bool {
	if description == "" {
		return false
	}
	encoded, err := json.Marshal(description)
	if err != nil {
		return false
	}
	needles := []string{description, url.QueryEscape(description)}
	if len(encoded) >= 2 {
		needles = append(needles, string(encoded[1:len(encoded)-1]))
	}
	for _, entry := range entries {
		if entry == nil {
			continue
		}
		values := []string{entry.Bytes}
		if decoded, err := base64.StdEncoding.DecodeString(entry.Bytes); err == nil {
			values = append(values, string(decoded))
		}
		for _, value := range values {
			for _, needle := range needles {
				if strings.Contains(value, needle) {
					return true
				}
			}
		}
	}
	return false
}

func readNetworkResponseBody(ctx context.Context, requestID network.RequestID) ([]byte, error) {
	var body []byte
	err := chromedp.Run(ctx, chromedp.ActionFunc(func(ctx context.Context) error {
		var err error
		body, err = network.GetResponseBody(requestID).Do(ctx)
		return err
	}))
	return body, err
}

func portalResponseError(body []byte) string {
	if len(body) == 0 {
		return ""
	}
	var response struct {
		Error json.RawMessage `json:"error"`
	}
	if json.Unmarshal(body, &response) != nil || len(response.Error) == 0 || string(response.Error) == "null" {
		return ""
	}
	var message string
	if json.Unmarshal(response.Error, &message) == nil {
		return message
	}
	var details struct {
		Message string `json:"message"`
	}
	if json.Unmarshal(response.Error, &details) == nil && details.Message != "" {
		return details.Message
	}
	return strings.TrimSpace(string(response.Error))
}

func portalResponseMessage(body []byte) string {
	if message := portalResponseError(body); message != "" {
		return message
	}
	var response struct {
		Message string `json:"message"`
	}
	if json.Unmarshal(body, &response) == nil {
		return response.Message
	}
	return ""
}

func visiblePortalAlert(ctx context.Context) string {
	const script = `(() => {
		const visible = (element) => {
			const style = getComputedStyle(element);
			const rect = element.getBoundingClientRect();
			return style.visibility !== "hidden" && style.display !== "none" && rect.width > 0 && rect.height > 0;
		};
		const messages = Array.from(document.querySelectorAll('[role="alert"], .flash-error, .flash'))
			.filter(visible)
			.map((element) => (element.textContent || "").trim())
			.filter(Boolean);
		return messages.join(" ");
	})()`
	var message string
	if err := chromedp.Run(ctx, chromedp.Evaluate(script, &message)); err != nil {
		return ""
	}
	return message
}

func portalFormDiagnostics(ctx context.Context) string {
	const script = `(() => {
		const visible = (element) => {
			const style = getComputedStyle(element);
			const rect = element.getBoundingClientRect();
			return style.visibility !== "hidden" && style.display !== "none" && rect.width > 0 && rect.height > 0;
		};
		const text = (element) => ((element.value || element.textContent || "") + "").trim();
		return JSON.stringify({
			fields: Array.from(document.querySelectorAll("textarea, input"))
				.filter(visible)
				.map((element) => ({ id: element.id, name: element.name, value: element.value })),
			actions: Array.from(document.querySelectorAll('button, [role="button"], input[type="button"], input[type="submit"]'))
				.filter(visible)
				.map((element) => ({ text: text(element), disabled: !!element.disabled }))
				.filter((element) => /(next|elevat|administrator|confirm|submit)/i.test(element.text)),
		});
	})()`
	var diagnostics string
	if err := chromedp.Run(ctx, chromedp.Evaluate(script, &diagnostics)); err != nil {
		return "unable to inspect visible portal controls: " + err.Error()
	}
	return "visible portal controls: " + diagnostics
}

func fillDescription(ctx context.Context, description string) error {
	const selector = `textarea#justification, textarea[name="justification"], textarea[aria-label*="Justification"]`
	if err := chromedp.Run(ctx,
		chromedp.WaitVisible(selector, chromedp.ByQuery),
		chromedp.Focus(selector, chromedp.ByQuery),
		chromedp.SendKeys(selector, description, chromedp.ByQuery),
	); err != nil {
		return fmt.Errorf("fill the elevation justification: %w", err)
	}
	return nil
}

func submitButtonScript(click bool) string {
	return fmt.Sprintf(`(() => {
	const visible = (element) => {
		const style = getComputedStyle(element);
		const rect = element.getBoundingClientRect();
		return style.visibility !== "hidden" && style.display !== "none" && rect.width > 0 && rect.height > 0;
	};
	const fieldLabel = (element) => {
		const direct = [element.name, element.id, element.placeholder, element.getAttribute("aria-label")]
			.filter(Boolean).join(" ");
		const associated = element.id
			? Array.from(document.querySelectorAll('label[for="' + CSS.escape(element.id) + '"]'))
				.map((candidate) => candidate.textContent || "").join(" ")
			: "";
		return (direct + " " + associated).toLowerCase();
	};
	const fields = Array.from(document.querySelectorAll('textarea, input[type="text"]')).filter(visible);
	const field = fields.find((candidate) => /(description|reason|justification)/.test(fieldLabel(candidate))) ||
		fields.find((candidate) => candidate.tagName.toLowerCase() === "textarea");
	if (!field) return false;
	const text = (element) => ((element.value || element.textContent || "") + "").trim().toLowerCase();
	const rank = (element) => {
		const label = text(element);
		if (label === "elevate to administrator") return 0;
		if (label === "elevate" || label === "request elevation") return 1;
		if (label === "confirm" || label === "submit") return 2;
		if (label.includes("elevat") || label.includes("administrator")) return 3;
		return 100;
	};
	const controls = Array.from(document.querySelectorAll('button, [role="button"], input[type="button"], input[type="submit"]'))
		.filter((element) => visible(element) && !element.disabled && rank(element) < 100)
		.sort((left, right) => rank(left) - rank(right));
	if (!controls.length) return false;
	if (%t) controls[0].click();
	return true;
})()`, click)
}
