// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package appinsights

import (
	"bytes"
	"cmp"
	"compress/gzip"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"slices"
	"time"

	"github.com/microsoft/go-infra/telemetry/internal/appinsights/internal/contracts"
)

type transmitter interface {
	transmit(ctx context.Context, items []batchItem) (*transmissionResponse, error)
}

type httpTransmitter struct {
	endpoint string
	client   *http.Client
}

type transmissionResponse struct {
	statusCode int
	retryAfter time.Time
	response   contracts.BackendResponse
}

const (
	successResponse                         = http.StatusOK
	partialSuccessResponse                  = http.StatusPartialContent
	requestTimeoutResponse                  = http.StatusRequestTimeout
	tooManyRequestsResponse                 = http.StatusTooManyRequests
	tooManyRequestsOverExtendedTimeResponse = 439
	errorResponse                           = http.StatusInternalServerError
	serviceUnavailableResponse              = http.StatusServiceUnavailable
)

func newTransmitter(endpointAddress string, client *http.Client) transmitter {
	if client == nil {
		client = http.DefaultClient
	}
	return &httpTransmitter{endpointAddress, client}
}

func (transmitter *httpTransmitter) transmit(ctx context.Context, items []batchItem) (*transmissionResponse, error) {
	// Serialize the items. It could be that some items can't be serialized,
	// in which case we will skip them and return an error together with the
	// transmission result.
	payload, jsonErr := serialize(items)
	if jsonErr != nil && payload == nil {
		return nil, jsonErr
	}

	// Compress the payload
	var postBody bytes.Buffer
	gzipWriter := gzip.NewWriter(&postBody)
	if _, err := gzipWriter.Write(payload); err != nil {
		gzipWriter.Close()
		return nil, fmt.Errorf("failed to compress the payload: %v", err)
	}

	if err := gzipWriter.Close(); err != nil {
		return nil, fmt.Errorf("failed to close gzip writer: %v", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, transmitter.endpoint, &postBody)
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %v", err)
	}

	req.Header.Set("Content-Encoding", "gzip")
	req.Header.Set("Content-Type", "application/x-json-stream")
	req.Header.Set("Accept-Encoding", "gzip, deflate")

	resp, err := transmitter.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("failed to send request: %v", err)
	}
	defer resp.Body.Close()

	result := &transmissionResponse{statusCode: resp.StatusCode}
	if retryAfterValue := resp.Header.Get("Retry-After"); retryAfterValue != "" {
		if result.retryAfter, err = time.Parse(time.RFC1123, retryAfterValue); err != nil {
			return nil, fmt.Errorf("failed to parse Retry-After header: %v", err)
		}
	}
	if err := json.NewDecoder(resp.Body).Decode(&result.response); err != nil {
		if errors.Is(err, io.EOF) {
			// Empty response is valid, possibly throttling.
			return result, nil
		}
		return nil, fmt.Errorf("failed to parse response: %v", err)
	}
	return result, jsonErr
}

func (resp *transmissionResponse) isSuccess() bool {
	return resp.statusCode == successResponse ||
		// Partial response but all items accepted
		(resp.statusCode == partialSuccessResponse &&
			resp.response.ItemsReceived == resp.response.ItemsAccepted)
}

func (resp *transmissionResponse) canRetry() bool {
	if resp.isSuccess() {
		return false
	}

	return resp.statusCode == partialSuccessResponse ||
		!resp.retryAfter.IsZero() ||
		(resp.statusCode == requestTimeoutResponse ||
			resp.statusCode == serviceUnavailableResponse ||
			resp.statusCode == errorResponse ||
			resp.statusCode == tooManyRequestsResponse ||
			resp.statusCode == tooManyRequestsOverExtendedTimeResponse)
}

func (resp *transmissionResponse) isThrottled() bool {
	return resp.statusCode == tooManyRequestsResponse ||
		resp.statusCode == tooManyRequestsOverExtendedTimeResponse ||
		!resp.retryAfter.IsZero()
}

func canRetryBackendError(berror contracts.BackendResponseError) bool {
	return berror.StatusCode == requestTimeoutResponse ||
		berror.StatusCode == serviceUnavailableResponse ||
		berror.StatusCode == errorResponse ||
		berror.StatusCode == tooManyRequestsResponse ||
		berror.StatusCode == tooManyRequestsOverExtendedTimeResponse
}

// result returns the number of succeeded and failed items, and a list of items that can be retried.
// Items is the complete list of result that was sent.
func (resp *transmissionResponse) result(items []batchItem) (succeed, failed int, retries []batchItem) {
	if resp.statusCode == partialSuccessResponse {
		// Make sure errors are ordered by index
		slices.SortFunc(resp.response.Errors, func(a, b contracts.BackendResponseError) int {
			return cmp.Compare(a.Index, b.Index)
		})

		retries = make([]batchItem, 0, len(resp.response.Errors))
		// Find each retryable error
		for _, responseResult := range resp.response.Errors {
			if responseResult.StatusCode == successResponse {
				succeed++
				continue
			}
			if !canRetryBackendError(responseResult) {
				failed++
				continue
			}
			if responseResult.Index >= len(items) {
				continue
			}
			retries = append(retries, items[responseResult.Index])
		}

		return succeed, failed, retries
	} else if resp.canRetry() {
		return 0, 0, items
	}
	return 0, len(items), nil
}
