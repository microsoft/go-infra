// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package main

import (
	"errors"
	"fmt"
	"syscall"
	"testing"

	"golang.org/x/net/http2"
)

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
			err: fmt.Errorf("wrapped: %w", http2.StreamError{
				StreamID: 1061,
				Code:     http2.ErrCodeCancel,
				Cause:    errors.New(http2ErrFromPeer),
			}),
			want: true,
		},
		{
			name: "other HTTP/2 stream error",
			err: http2.StreamError{
				StreamID: 1061,
				Code:     http2.ErrCodeCancel,
				Cause:    errors.New("locally cancelled"),
			},
		},
		{
			name: "different HTTP/2 error code",
			err: http2.StreamError{
				StreamID: 1061,
				Code:     http2.ErrCodeProtocol,
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
