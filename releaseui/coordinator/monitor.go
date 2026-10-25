// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package coordinator

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"time"
)

const monitorInterval = 5 * time.Second

// Observation is a read-only status for one step's known external operation.
// A failed or canceled operation is data; a failure to read it is returned as an error instead.
type Observation struct {
	Status   StepStatus
	Progress StepProgress
	Error    string
}

// Terminal reports whether the observed operation has finished.
func (o Observation) Terminal() bool {
	return o.Status == StepStatusSucceeded || o.Status == StepStatusFailed || o.Status == StepStatusCanceled
}

// Validate checks an observed status and its optional external-operation link.
func (o Observation) Validate() error {
	switch o.Status {
	case StepStatusWaiting, StepStatusRunning, StepStatusSucceeded, StepStatusFailed, StepStatusCanceled:
	default:
		return fmt.Errorf("invalid observed step status %q", o.Status)
	}
	return validateRunURL(o.Progress.RunURL)
}

func validateRunURL(runURL string) error {
	if runURL != "" {
		link, err := url.Parse(runURL)
		if err != nil || link.Scheme != "https" || link.Host == "" || link.User != nil {
			return errors.New("external operation has an invalid HTTPS link")
		}
	}
	return nil
}

// NewMonitor creates a step whose execution and read-only view share the same read and describe
// callbacks. record runs only during execution, after a successful read, before interpreting its
// outcome. It may checkpoint process-owned data and may be nil.
func NewMonitor[T any](
	name string,
	timeout time.Duration,
	read func(context.Context) (T, error),
	describe func(T) (*Observation, error),
	record func(context.Context, T) error,
) (*Step, error) {
	if read == nil || describe == nil {
		return nil, errors.New("monitor read and description callbacks are required")
	}
	check := func(ctx context.Context) (T, *Observation, error) {
		if err := ctx.Err(); err != nil {
			var value T
			return value, nil, err
		}
		value, err := read(ctx)
		if err != nil {
			return value, nil, err
		}
		if err := ctx.Err(); err != nil {
			return value, nil, err
		}
		observation, err := describe(value)
		if err == nil && observation != nil {
			err = observation.Validate()
		}
		return value, observation, err
	}
	step := NewRootStep(name, timeout, func(ctx context.Context) error {
		for {
			value, observation, err := check(ctx)
			if err != nil {
				return err
			}
			if observation == nil {
				return errors.New("monitor has no known external operation")
			}
			if err := ctx.Err(); err != nil {
				return err
			}
			if record != nil {
				if err := record(ctx, value); err != nil {
					return err
				}
			}
			if err := ctx.Err(); err != nil {
				return err
			}
			ReportProgress(ctx, observation.Progress)
			switch observation.Status {
			case StepStatusSucceeded:
				return nil
			case StepStatusFailed, StepStatusCanceled:
				detail := observation.Error
				if detail == "" {
					detail = fmt.Sprintf("external operation %s", observation.Status)
				}
				if observation.Progress.RunURL != "" {
					detail += ": " + observation.Progress.RunURL
				}
				if observation.Status == StepStatusCanceled {
					return fmt.Errorf("%w: %s", ErrCanceled, detail)
				}
				return errors.New(detail)
			}
			timer := time.NewTimer(monitorInterval)
			select {
			case <-ctx.Done():
				timer.Stop()
				return ctx.Err()
			case <-timer.C:
			}
		}
	})
	step.Observe = func(ctx context.Context) (*Observation, error) {
		_, observation, err := check(ctx)
		return observation, err
	}
	return step, nil
}
