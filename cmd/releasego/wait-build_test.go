// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package main

import (
	"errors"
	"fmt"
	"syscall"
	"testing"
)

type testHTTP2StreamError struct {
	StreamID uint32
	Code     uint32
	Cause    error
}

func (e testHTTP2StreamError) Error() string { return "HTTP/2 stream error" }

func (e testHTTP2StreamError) As(target any) bool {
	streamError, ok := target.(*http2StreamError)
	if !ok {
		return false
	}
	*streamError = http2StreamError(e)
	return true
}

func TestIsKnownAPIFlakiness(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want bool
	}{
		{
			name: "connection reset",
			err:  fmt.Errorf("wrapped: %w", syscall.ECONNRESET),
			want: true,
		},
		{
			name: "peer cancelled HTTP/2 stream",
			err: fmt.Errorf("wrapped: %w", testHTTP2StreamError{
				StreamID: 1061,
				Code:     http2ErrCodeCancel,
				Cause:    errors.New(http2ErrFromPeer),
			}),
			want: true,
		},
		{
			name: "other HTTP/2 stream error",
			err: testHTTP2StreamError{
				StreamID: 1061,
				Code:     http2ErrCodeCancel,
				Cause:    errors.New("locally cancelled"),
			},
		},
		{
			name: "different HTTP/2 error code",
			err: testHTTP2StreamError{
				StreamID: 1061,
				Code:     0x1,
				Cause:    errors.New(http2ErrFromPeer),
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := isKnownAPIFlakiness(tt.err); got != tt.want {
				t.Errorf("isKnownAPIFlakiness(%v) = %t, want %t", tt.err, got, tt.want)
			}
		})
	}
}
