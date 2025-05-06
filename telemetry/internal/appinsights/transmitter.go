package appinsights

import (
	"bytes"
	"cmp"
	"compress/gzip"
	"encoding/json"
	"io"
	"log"
	"net/http"
	"slices"
	"time"

	"github.com/microsoft/go-infra/telemetry/internal/appinsights/internal/contracts"
)

type transmitter interface {
	Transmit(payload []byte, items []*contracts.Envelope) (*transmissionResult, error)
}

type httpTransmitter struct {
	endpoint string
	client   *http.Client
}

type transmissionResult struct {
	statusCode int
	retryAfter time.Time
	response   backendResponse
}

// Structures returned by data collector
type backendResponse struct {
	ItemsReceived int                      `json:"itemsReceived"`
	ItemsAccepted int                      `json:"itemsAccepted"`
	Errors        []itemTransmissionResult `json:"errors"`
}

type itemTransmissionResult struct {
	Index      int    `json:"index"`
	StatusCode int    `json:"statusCode"`
	Message    string `json:"message"`
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

func (transmitter *httpTransmitter) Transmit(payload []byte, items []*contracts.Envelope) (*transmissionResult, error) {
	log.Printf("--------- Transmitting %d items ---------", len(items))
	startTime := time.Now()

	// Compress the payload
	var postBody bytes.Buffer
	gzipWriter := gzip.NewWriter(&postBody)
	if _, err := gzipWriter.Write(payload); err != nil {
		log.Printf("Failed to compress the payload: %v", err)
		gzipWriter.Close()
		return nil, err
	}

	gzipWriter.Close()

	req, err := http.NewRequest(http.MethodPost, transmitter.endpoint, &postBody)
	if err != nil {
		return nil, err
	}

	req.Header.Set("Content-Encoding", "gzip")
	req.Header.Set("Content-Type", "application/x-json-stream")
	req.Header.Set("Accept-Encoding", "gzip, deflate")

	resp, err := transmitter.client.Do(req)
	if err != nil {
		log.Printf("Failed to transmit telemetry: %v", err)
		return nil, err
	}
	defer resp.Body.Close()

	log.Printf("Telemetry transmitted in %v", time.Since(startTime))
	log.Printf("Response: %d", resp.StatusCode)

	result := &transmissionResult{statusCode: resp.StatusCode}
	// Grab Retry-After header
	if retryAfterValue := resp.Header.Get("Retry-After"); retryAfterValue != "" {
		if result.retryAfter, err = time.Parse(time.RFC1123, retryAfterValue); err != nil {
			log.Printf("Failed to parse Retry-After header: %v", err)
			return nil, err
		}
	}

	// Parse body, if possible
	if err := json.NewDecoder(resp.Body).Decode(&result.response); err != nil {
		if err == io.EOF {
			// Empty response is valid, possible throttling.
			return result, nil
		}
		log.Printf("Failed to parse response: %v", err)
		return nil, err
	}

	// Write diagnostics
	log.Printf("Items accepted/received: %d/%d", result.response.ItemsAccepted, result.response.ItemsReceived)
	if len(result.response.Errors) > 0 {
		log.Printf("Errors:")
		for _, err := range result.response.Errors {
			if err.Index < len(items) {
				log.Printf("#%d - %d %s", err.Index, err.StatusCode, err.Message)
				log.Printf("Telemetry item:\n\t%s", string(serialize(items[err.Index:err.Index+1])))
			}
		}
	}

	return result, nil
}

func (result *transmissionResult) isSuccess() bool {
	return result.statusCode == successResponse ||
		// Partial response but all items accepted
		(result.statusCode == partialSuccessResponse &&
			result.response.ItemsReceived == result.response.ItemsAccepted)
}

func (result *transmissionResult) isFailure() bool {
	return result.statusCode != successResponse && result.statusCode != partialSuccessResponse
}

func (result *transmissionResult) canRetry() bool {
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

func (result *transmissionResult) isPartialSuccess() bool {
	return result.statusCode == partialSuccessResponse &&
		result.response.ItemsReceived != result.response.ItemsAccepted
}

func (result *transmissionResult) isThrottled() bool {
	return result.statusCode == tooManyRequestsResponse ||
		result.statusCode == tooManyRequestsOverExtendedTimeResponse ||
		!result.retryAfter.IsZero()
}

func (result itemTransmissionResult) canRetry() bool {
	return result.StatusCode == requestTimeoutResponse ||
		result.StatusCode == serviceUnavailableResponse ||
		result.StatusCode == errorResponse ||
		result.StatusCode == tooManyRequestsResponse ||
		result.StatusCode == tooManyRequestsOverExtendedTimeResponse
}

func (result *transmissionResult) getRetryItems(payload []byte, items []*contracts.Envelope) ([]byte, []*contracts.Envelope) {
	if result.statusCode == partialSuccessResponse {
		// Make sure errors are ordered by index
		slices.SortFunc(result.response.Errors, func(a, b itemTransmissionResult) int {
			return cmp.Compare(a.Index, b.Index)
		})

		var resultPayload bytes.Buffer
		resultItems := make([]*contracts.Envelope, 0)
		ptr := 0
		idx := 0

		// Find each retryable error
		for _, responseResult := range result.response.Errors {
			if !responseResult.canRetry() {
				continue
			}
			// Advance ptr to start of desired line
			for ; idx < responseResult.Index && ptr < len(payload); ptr++ {
				if payload[ptr] == '\n' {
					idx++
				}
			}

			startPtr := ptr

			// Read to end of line
			for ; idx == responseResult.Index && ptr < len(payload); ptr++ {
				if payload[ptr] == '\n' {
					idx++
				}
			}

			// Copy item into output buffer
			resultPayload.Write(payload[startPtr:ptr])
			resultItems = append(resultItems, items[responseResult.Index])
		}

		return resultPayload.Bytes(), resultItems
	} else if result.canRetry() {
		return payload, items
	} else {
		return payload[:0], items[:0]
	}
}
