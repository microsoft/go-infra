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
		return nil, fmt.Errorf("failed to transmit telemetry: %v", err)
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

func (result *transmissionResponse) isSuccess() bool {
	return result.statusCode == successResponse ||
		// Partial response but all items accepted
		(result.statusCode == partialSuccessResponse &&
			result.response.ItemsReceived == result.response.ItemsAccepted)
}

func (result *transmissionResponse) isFailure() bool {
	return result.statusCode != successResponse && result.statusCode != partialSuccessResponse
}

func (result *transmissionResponse) canRetry() bool {
	if result.isSuccess() {
		return false
	}

	return result.statusCode == partialSuccessResponse ||
		!result.retryAfter.IsZero() ||
		(result.statusCode == requestTimeoutResponse ||
			result.statusCode == serviceUnavailableResponse ||
			result.statusCode == errorResponse ||
			result.statusCode == tooManyRequestsResponse ||
			result.statusCode == tooManyRequestsOverExtendedTimeResponse)
}

func (result *transmissionResponse) isThrottled() bool {
	return result.statusCode == tooManyRequestsResponse ||
		result.statusCode == tooManyRequestsOverExtendedTimeResponse ||
		!result.retryAfter.IsZero()
}

func canRetryBackendError(berror contracts.BackendResponseError) bool {
	return berror.StatusCode == requestTimeoutResponse ||
		berror.StatusCode == serviceUnavailableResponse ||
		berror.StatusCode == errorResponse ||
		berror.StatusCode == tooManyRequestsResponse ||
		berror.StatusCode == tooManyRequestsOverExtendedTimeResponse
}

func (result *transmissionResponse) getRetryItems(items []batchItem) []batchItem {
	if result.statusCode == partialSuccessResponse {
		// Make sure errors are ordered by index
		slices.SortFunc(result.response.Errors, func(a, b contracts.BackendResponseError) int {
			return cmp.Compare(a.Index, b.Index)
		})

		resultItems := make([]batchItem, 0, len(result.response.Errors))
		// Find each retryable error
		for _, responseResult := range result.response.Errors {
			if !canRetryBackendError(responseResult) {
				continue
			}
			if responseResult.Index >= len(items) {
				continue
			}
			resultItems = append(resultItems, items[responseResult.Index])
		}

		return resultItems
	} else if result.canRetry() {
		return items
	}
	return nil
}
