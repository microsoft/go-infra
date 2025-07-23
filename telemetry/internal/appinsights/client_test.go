// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

//go:build (go1.24 && goexperiment.synctest) || go1.25

package appinsights_test

import (
	"bytes"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/microsoft/go-infra/telemetry/internal/appinsights"
	"github.com/microsoft/go-infra/telemetry/internal/appinsights/internal/appinsightstest"
	"github.com/microsoft/go-infra/telemetry/internal/appinsights/internal/contracts"
)

const test_ikey = "01234567-0000-89ab-cdef-000000000000"

type testPlan struct {
	maxBatchSize     int
	maxBatchInterval time.Duration
	actions          []appinsightstest.Action
	responses        []appinsightstest.ServerResponse
	itemsIgnored     int
}

func (plan testPlan) run(t *testing.T) {
	syncRun(t, func(t *testing.T) {
		client := appinsightstest.New(t, plan.actions, plan.responses...)
		client.SetMaxBatchSize(plan.maxBatchSize)
		client.SetMaxBatchInterval(plan.maxBatchInterval)

		client.Act()
		err := client.Close(t.Context())
		if plan.itemsIgnored > 0 {
			want := fmt.Sprintf(`failed to transmit %d telemetry items:`, plan.itemsIgnored)
			if err == nil {
				t.Errorf("expected error %q, got nil", want)
			} else if !strings.Contains(err.Error(), want) {
				t.Errorf("expected error %q, got %q", want, err)
			}
		} else if err != nil {
			t.Errorf("unexpected error: %v", err)
		}
	})
}

func TestClientNoUpload(t *testing.T) {
	plan := testPlan{
		maxBatchSize: 2,
		actions: []appinsightstest.Action{
			{Type: appinsightstest.TrackAction},
			{Type: appinsightstest.StopAction},
		},
	}
	plan.run(t)
}

func TestClientBatch(t *testing.T) {
	plan := testPlan{
		maxBatchSize: 2,
		actions: []appinsightstest.Action{
			{Type: appinsightstest.TrackAction},
			{Type: appinsightstest.TrackAction},
			{Type: appinsightstest.TrackAction}, // this one is ignored
		},
		responses: []appinsightstest.ServerResponse{
			{StatusCode: http.StatusOK, EventIndices: []int{0, 1}},
		},
		itemsIgnored: 1,
	}
	plan.run(t)
}

func TestClientBatchMultiple(t *testing.T) {
	plan := testPlan{
		maxBatchSize: 2,
		actions: []appinsightstest.Action{
			{Type: appinsightstest.TrackAction},
			{Type: appinsightstest.TrackAction},
			{Type: appinsightstest.TrackAction},
			{Type: appinsightstest.TrackAction},
			{Type: appinsightstest.TrackAction}, // this one is ignored
		},
		responses: []appinsightstest.ServerResponse{
			{StatusCode: http.StatusOK, EventIndices: []int{0, 1}},
			{StatusCode: http.StatusOK, EventIndices: []int{2, 3}},
		},
		itemsIgnored: 1,
	}
	plan.run(t)
}

func TestClientInterval(t *testing.T) {
	plan := testPlan{
		maxBatchInterval: 1 * time.Second,
		actions: []appinsightstest.Action{
			{Type: appinsightstest.TrackAction, Delay: 500 * time.Millisecond},
			{Type: appinsightstest.TrackAction, Delay: 500 * time.Millisecond},
			{Type: appinsightstest.TrackAction, Delay: 500 * time.Millisecond}, // this one is ignored
		},
		responses: []appinsightstest.ServerResponse{
			{StatusCode: http.StatusOK, EventIndices: []int{0, 1}},
		},
	}
	plan.run(t)
}

func TestClientIntervalMultiple(t *testing.T) {
	plan := testPlan{
		maxBatchInterval: 1 * time.Second,
		actions: []appinsightstest.Action{
			{Type: appinsightstest.TrackAction, Delay: 500 * time.Millisecond},
			{Type: appinsightstest.TrackAction, Delay: 500 * time.Millisecond},
			{Type: appinsightstest.TrackAction, Delay: 1 * time.Second},
			{Type: appinsightstest.TrackAction, Delay: 500 * time.Second}, // this one is ignored
		},
		responses: []appinsightstest.ServerResponse{
			{StatusCode: http.StatusOK, EventIndices: []int{0, 1}},
			{StatusCode: http.StatusOK, EventIndices: []int{2}},
		},
	}
	plan.run(t)
}

func TestClientFlush(t *testing.T) {
	plan := testPlan{
		maxBatchSize: 2,
		actions: []appinsightstest.Action{
			{Type: appinsightstest.TrackAction},
			{Type: appinsightstest.TrackAction},
			{Type: appinsightstest.FlushAction},
		},
		responses: []appinsightstest.ServerResponse{
			{StatusCode: http.StatusOK, EventIndices: []int{0, 1}},
		},
	}
	plan.run(t)
}

func TestClientFlushMultiple(t *testing.T) {
	plan := testPlan{
		actions: []appinsightstest.Action{
			{Type: appinsightstest.FlushAction}, // does nothing
			{Type: appinsightstest.TrackAction},
			{Type: appinsightstest.FlushAction},
			{Type: appinsightstest.TrackAction},
			{Type: appinsightstest.FlushAction},
		},
		responses: []appinsightstest.ServerResponse{
			{StatusCode: http.StatusOK, EventIndices: []int{0}},
			{StatusCode: http.StatusOK, EventIndices: []int{1}},
		},
	}
	plan.run(t)
}

func TestClientBatchIntervalFlush(t *testing.T) {
	plan := testPlan{
		maxBatchSize:     2,
		maxBatchInterval: 1 * time.Second,
		actions: []appinsightstest.Action{
			{Type: appinsightstest.TrackAction},
			{Type: appinsightstest.TrackAction, Delay: 1 * time.Second},        // uploads 0 due to interval
			{Type: appinsightstest.TrackAction},                                // uploads 1 and 2 due to batch
			{Type: appinsightstest.TrackAction, Delay: 500 * time.Millisecond}, // uploads nothing
			{Type: appinsightstest.TrackAction, Delay: 500 * time.Millisecond}, // uploads 3 and 4
			{Type: appinsightstest.TrackAction},                                // uploads nothing
			{Type: appinsightstest.TrackAction, Delay: 2 * time.Second},        // uploads 5
			{Type: appinsightstest.FlushAction},                                // uploads 6
			{Type: appinsightstest.TrackAction},                                // ignored
		},
		responses: []appinsightstest.ServerResponse{
			{StatusCode: http.StatusOK, EventIndices: []int{0}},    // interval
			{StatusCode: http.StatusOK, EventIndices: []int{1, 2}}, // batch
			{StatusCode: http.StatusOK, EventIndices: []int{3, 4}}, // batch
			{StatusCode: http.StatusOK, EventIndices: []int{5}},    // interval
			{StatusCode: http.StatusOK, EventIndices: []int{6}},    // flush
		},
	}
	plan.run(t)
}

func TestClientFailNoRetry(t *testing.T) {
	plan := testPlan{
		actions: []appinsightstest.Action{
			{Type: appinsightstest.TrackAction},
			{Type: appinsightstest.TrackAction},
			{Type: appinsightstest.FlushAction},
		},
		responses: []appinsightstest.ServerResponse{
			{StatusCode: http.StatusBadRequest, EventIndices: []int{0, 1}},
		},
	}
	plan.run(t)
}

func TestClientFailRetryAndSucceed(t *testing.T) {
	plan := testPlan{
		maxBatchInterval: 1 * time.Second,
		actions: []appinsightstest.Action{
			{Type: appinsightstest.TrackAction},
			{Type: appinsightstest.TrackAction},
			{Type: appinsightstest.FlushAction},
			{Type: appinsightstest.SleepAction, Delay: 5 * time.Second},
		},
		responses: []appinsightstest.ServerResponse{
			{StatusCode: http.StatusInternalServerError, EventIndices: []int{0, 1}},
			{StatusCode: http.StatusOK, EventIndices: []int{0, 1}},
		},
	}
	plan.run(t)
}

func TestClientFailRetry(t *testing.T) {
	// Verify that the events are only retried 3 times.
	plan := testPlan{
		maxBatchInterval: 1 * time.Second,
		actions: []appinsightstest.Action{
			{Type: appinsightstest.TrackAction},
			{Type: appinsightstest.TrackAction},
			{Type: appinsightstest.FlushAction},
			{Type: appinsightstest.SleepAction, Delay: 5 * time.Second},
		},
		responses: []appinsightstest.ServerResponse{
			{StatusCode: http.StatusInternalServerError, EventIndices: []int{0, 1}},
			{StatusCode: http.StatusInternalServerError, EventIndices: []int{0, 1}},
			{StatusCode: http.StatusInternalServerError, EventIndices: []int{0, 1}},
			{StatusCode: http.StatusInternalServerError, EventIndices: []int{0, 1}},
		},
	}
	plan.run(t)
}

func TestClientPartialFailNoRetry(t *testing.T) {
	plan := testPlan{
		maxBatchInterval: 1 * time.Second,
		actions: []appinsightstest.Action{
			{Type: appinsightstest.TrackAction},
			{Type: appinsightstest.TrackAction},
			{Type: appinsightstest.FlushAction},
			{Type: appinsightstest.SleepAction, Delay: 5 * time.Second},
		},
		responses: []appinsightstest.ServerResponse{
			{
				StatusCode:   http.StatusPartialContent,
				EventIndices: []int{0, 1},
				Errors: []contracts.BackendResponseError{
					{Index: 0, StatusCode: http.StatusOK},
					{Index: 1, StatusCode: http.StatusBadRequest},
				},
			},
		},
	}
	plan.run(t)
}

func TestClientPartialFailRetryAndSucceed(t *testing.T) {
	plan := testPlan{
		maxBatchInterval: 1 * time.Second,
		actions: []appinsightstest.Action{
			{Type: appinsightstest.TrackAction},
			{Type: appinsightstest.TrackAction},
			{Type: appinsightstest.FlushAction},
			{Type: appinsightstest.SleepAction, Delay: 5 * time.Second},
		},
		responses: []appinsightstest.ServerResponse{
			{
				StatusCode:   http.StatusPartialContent,
				EventIndices: []int{0, 1},
				Errors: []contracts.BackendResponseError{
					{Index: 0, StatusCode: http.StatusInternalServerError},
					{Index: 1, StatusCode: http.StatusOK},
				},
			},
			{StatusCode: http.StatusOK, EventIndices: []int{0}},
		},
	}
	plan.run(t)
}

func TestClientPartialFailRetry(t *testing.T) {
	plan := testPlan{
		maxBatchInterval: 1 * time.Second,
		actions: []appinsightstest.Action{
			{Type: appinsightstest.TrackAction},
			{Type: appinsightstest.TrackAction},
			{Type: appinsightstest.FlushAction},
			{Type: appinsightstest.SleepAction, Delay: 5 * time.Second},
		},
		responses: []appinsightstest.ServerResponse{
			{
				StatusCode:   http.StatusPartialContent,
				EventIndices: []int{0, 1},
				Errors: []contracts.BackendResponseError{
					{Index: 0, StatusCode: http.StatusInternalServerError},
					{Index: 1, StatusCode: http.StatusOK},
				},
			},
			{StatusCode: http.StatusInternalServerError, EventIndices: []int{0}},
			{StatusCode: http.StatusInternalServerError, EventIndices: []int{0}},
			{StatusCode: http.StatusInternalServerError, EventIndices: []int{0}},
		},
	}
	plan.run(t)
}

func TestClientFlushAfterStop(t *testing.T) {
	plan := testPlan{
		maxBatchSize: 2,
		actions: []appinsightstest.Action{
			{Type: appinsightstest.TrackAction},
			{Type: appinsightstest.StopAction},
			{Type: appinsightstest.FlushAction},
		},
	}
	plan.run(t)
}

func TestClientThrottle(t *testing.T) {
	plan := testPlan{
		maxBatchSize:     1,
		maxBatchInterval: 1 * time.Second,
		actions: []appinsightstest.Action{
			{Type: appinsightstest.TrackAction}, // throttled
			{Type: appinsightstest.FlushAction},
			{Type: appinsightstest.TrackAction, Delay: 5 * time.Second}, // dropped
			{Type: appinsightstest.FlushAction},
			{Type: appinsightstest.CloseAction, Error: "dropped 1 telemetry items due to throttling"},
		},
		responses: []appinsightstest.ServerResponse{
			{
				StatusCode:   http.StatusTooManyRequests,
				EventIndices: []int{0},
				RetryDelay:   5 * time.Second,
			},
			{StatusCode: http.StatusOK, EventIndices: []int{0}},
		},
	}
	plan.run(t)
}

func TestClientRetriableStatusCodes(t *testing.T) {
	plan := testPlan{
		maxBatchInterval: 1 * time.Second,
		actions: []appinsightstest.Action{
			{Type: appinsightstest.TrackAction},
			{Type: appinsightstest.FlushAction},
			{Type: appinsightstest.TrackAction, Delay: 2 * time.Second},
			{Type: appinsightstest.FlushAction},
			{Type: appinsightstest.TrackAction, Delay: 2 * time.Second},
			{Type: appinsightstest.FlushAction},
			{Type: appinsightstest.TrackAction, Delay: 2 * time.Second},
			{Type: appinsightstest.FlushAction},
			{Type: appinsightstest.TrackAction, Delay: 2 * time.Second},
			{Type: appinsightstest.FlushAction},
			{Type: appinsightstest.SleepAction, Delay: 2 * time.Second},
		},
		responses: []appinsightstest.ServerResponse{
			{StatusCode: http.StatusRequestTimeout, EventIndices: []int{0}},
			{StatusCode: http.StatusOK, EventIndices: []int{0}},
			{StatusCode: http.StatusInternalServerError, EventIndices: []int{1}},
			{StatusCode: http.StatusOK, EventIndices: []int{1}},
			{StatusCode: http.StatusServiceUnavailable, EventIndices: []int{2}},
			{StatusCode: http.StatusOK, EventIndices: []int{2}},
			{StatusCode: http.StatusTooManyRequests, EventIndices: []int{3}},
			{StatusCode: http.StatusOK, EventIndices: []int{3}},
			{StatusCode: 439, EventIndices: []int{4}}, // TooManyRequestsOverExtendedTimeResponse
			{StatusCode: http.StatusOK, EventIndices: []int{4}},
		},
	}
	plan.run(t)
}

type fakeTransport struct {
	code int
	body []byte
	err  error
}

func (t fakeTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	return &http.Response{
		StatusCode: t.code,
		Body:       io.NopCloser(bytes.NewBuffer(t.body)),
	}, t.err
}

func BenchmarkClientBurstPerformance(b *testing.B) {
	client := &appinsights.Client{
		InstrumentationKey: test_ikey,
		HTTPClient: &http.Client{
			Transport: fakeTransport{
				code: http.StatusOK,
				body: []byte(`{"itemsReceived":4, "itemsAccepted":4, "errors":[]}`),
			},
		},
	}

	for b.Loop() {
		client.TrackEvent("A message", nil)
	}

	client.Close(b.Context())
}
