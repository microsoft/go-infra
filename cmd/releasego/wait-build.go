// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log"
	"net/http"
	"syscall"
	"time"

	"github.com/microsoft/azure-devops-go-api/azuredevops/v7"
	"github.com/microsoft/azure-devops-go-api/azuredevops/v7/build"
	"github.com/microsoft/go-infra/azdo"
	"github.com/microsoft/go-infra/subcmd"
	"golang.org/x/net/http2"
)

func init() {
	subcommands = append(subcommands, subcmd.Option{
		Name:    "wait-build",
		Summary: "Wait until the AzDO Pipeline build is complete and successful.",
		Description: `
Transient API failures and unexpected non-JSON successful responses are retried
up to ten consecutive times. Non-JSON responses include their full body in errors
and logs.
`,
		Handle: handleWaitBuild,
	})
}

func handleWaitBuild(p subcmd.ParseFunc) error {
	id := flag.Int("id", 0, "[Required] The AzDO build ID (not build number) to query.")
	pollDelaySeconds := flag.Int("poll-delay", 5, "Number of seconds to wait between each poll attempt.")
	azdoFlags := azdo.BindClientFlags()

	if err := p(); err != nil {
		return err
	}

	if *id == 0 {
		flag.Usage()
		log.Fatalln("No ID specified.")
	}
	if err := azdoFlags.EnsureAssigned(); err != nil {
		flag.Usage()
		return err
	}

	pollDelay := time.Duration(*pollDelaySeconds) * time.Second

	ctx := context.Background()
	const maxSequentialFlakyErrors = 10
	sequentialFlakyErrors := 0
	retryFlakyError := func(action string, err error) error {
		if !isKnownAPIFlakiness(err) {
			return err
		}
		sequentialFlakyErrors++
		if sequentialFlakyErrors > maxSequentialFlakyErrors {
			return fmt.Errorf("too many sequential flaky errors: %w", err)
		}
		log.Printf("Encountered suspected AzDO API flakiness when %s, next poll in %v: %v", action, pollDelay, err)
		time.Sleep(pollDelay)
		return nil
	}

	var c build.Client
	for {
		var err error
		c, err = azdo.NewBuildClient(ctx, azdoFlags.NewConnection())
		if err == nil {
			break
		}
		if err := retryFlakyError("initializing build client", err); err != nil {
			return err
		}
	}
	sequentialFlakyErrors = 0

	for {
		b, err := c.GetBuild(ctx, build.GetBuildArgs{
			BuildId: id,
			Project: azdoFlags.Proj,
		})
		if err != nil {
			if err := retryFlakyError("querying build", err); err != nil {
				return err
			}
			continue
		}
		sequentialFlakyErrors = 0

		if b == nil || b.Status == nil {
			return fmt.Errorf("build %d response did not contain a status", *id)
		}
		url, _ := azdo.GetBuildWebURL(b)

		if *b.Status != build.BuildStatusValues.Completed {
			log.Printf("Build status: %v, next poll in %v... %v\n", *b.Status, pollDelay, url)
			time.Sleep(pollDelay)
			continue
		}

		if b.Result == nil {
			return fmt.Errorf("completed build %d response did not contain a result", *id)
		}
		if *b.Result != build.BuildResultValues.Succeeded &&
			*b.Result != build.BuildResultValues.PartiallySucceeded {

			return fmt.Errorf("build completed, but was not successful: result %q %v", *b.Result, url)
		}

		log.Printf("Build completed! %v", url)
		log.Printf("Success. Result: %q\n", *b.Result)
		break
	}

	return nil
}

func isKnownAPIFlakiness(err error) bool {
	if errors.Is(err, syscall.ECONNRESET) {
		return true
	}

	if response, ok := errors.AsType[*azdo.NonJSONResponseError](err); ok && response != nil {
		return response.StatusCode >= 200 && response.StatusCode < 300 || retryableHTTPStatus(response.StatusCode)
	}
	if response, ok := errors.AsType[azuredevops.WrappedError](err); ok && response.StatusCode != nil {
		return retryableHTTPStatus(*response.StatusCode)
	}
	if response, ok := errors.AsType[*azuredevops.WrappedError](err); ok && response != nil && response.StatusCode != nil {
		return retryableHTTPStatus(*response.StatusCode)
	}

	var streamError http2.StreamError
	return errors.As(err, &streamError) &&
		streamError.Code == http2.ErrCodeCancel &&
		streamError.Cause != nil &&
		streamError.Cause.Error() == http2ErrFromPeer
}

func retryableHTTPStatus(status int) bool {
	return status == http.StatusTooManyRequests || status >= 500 && status < 600
}

const (
	http2ErrFromPeer = "received from peer"
)
