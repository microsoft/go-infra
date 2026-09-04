// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package releaseui

import (
	"context"
	"errors"
	"fmt"
	"strconv"

	"github.com/microsoft/go-infra/cmd/releaseagent/internal/goimagesworkflow"
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

var _ goimagesworkflow.Service = disabledGoImagesService{}

type restoredRunMonitor struct {
	buildID int
	monitor func(context.Context, int) error
}

func (restoredRunMonitor) PollMirror(context.Context, string) error {
	return errors.New("a restored-run monitor cannot verify a source mirror")
}

func (restoredRunMonitor) QueuePipeline(context.Context, map[string]string) (string, error) {
	return "", errors.New("a restored-run monitor cannot queue a pipeline")
}

func (monitor restoredRunMonitor) PollPipeline(ctx context.Context, buildID string) error {
	id, err := strconv.Atoi(buildID)
	if err != nil || id != monitor.buildID {
		return fmt.Errorf("monitor build ID %q does not match restored build %d", buildID, monitor.buildID)
	}
	return monitor.monitor(ctx, id)
}

var _ goimagesworkflow.Service = restoredRunMonitor{}
