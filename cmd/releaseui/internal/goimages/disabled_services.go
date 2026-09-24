// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package goimages

import (
	"context"
	"errors"
)

// ErrExternalExecutionDisabled prevents a prepared plan from contacting external services.
var ErrExternalExecutionDisabled = errors.New("external release execution is disabled")

type disabledGoImagesService struct{}

func (disabledGoImagesService) PollMirror(context.Context, string) error {
	return ErrExternalExecutionDisabled
}

func (disabledGoImagesService) QueuePipeline(context.Context, map[string]string) (string, error) {
	return "", ErrExternalExecutionDisabled
}

func (disabledGoImagesService) PollPipeline(context.Context, string) error {
	return ErrExternalExecutionDisabled
}

var _ RunService = disabledGoImagesService{}
