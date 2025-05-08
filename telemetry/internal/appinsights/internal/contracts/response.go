// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package contracts

import "fmt"

const (
	tooManyRequestsOverExtendedTimeResponse = 439
)

// BackendResponse represents the response from the Application Insights backend.
type BackendResponse struct {
	ItemsReceived int                    `json:"itemsReceived"`
	ItemsAccepted int                    `json:"itemsAccepted"`
	Errors        []BackendResponseError `json:"errors"`
}

func (r *BackendResponse) IsSucess() bool {
	return r.ItemsReceived == r.ItemsAccepted
}

// BackendResponseError represents an error in the response from the Application Insights backend.
type BackendResponseError struct {
	Index      int    `json:"index"`
	StatusCode int    `json:"statusCode"`
	Message    string `json:"message"`
}

func (r BackendResponseError) Error() string {
	return fmt.Errorf("index: %d, statusCode: %d, message: %s", r.Index, r.StatusCode, r.Message).Error()
}
