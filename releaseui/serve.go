// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package releaseui

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"time"
)

const (
	readHeaderTimeout = 10 * time.Second
	idleTimeout       = 2 * time.Minute
	shutdownTimeout   = 5 * time.Second
)

// ListenAndServe runs the release UI on a loopback address until ctx is canceled or the HTTP
// server stops. The ready callback receives the one-time launch URL after the listener starts.
func ListenAndServe(ctx context.Context, address string, ready func(string), options ...Option) error {
	listener, err := net.Listen("tcp", address)
	if err != nil {
		return fmt.Errorf("listen on %q: %w", address, err)
	}
	defer listener.Close()

	tcpAddress, ok := listener.Addr().(*net.TCPAddr)
	if !ok || !tcpAddress.IP.IsLoopback() {
		return fmt.Errorf("refusing to serve release UI on non-loopback address %q", listener.Addr())
	}
	ui, err := New(ctx, options...)
	if err != nil {
		return err
	}
	launchURL, err := ui.LaunchURL("http://" + listener.Addr().String())
	if err != nil {
		return err
	}

	server := &http.Server{
		Handler:           ui.Handler(),
		ReadHeaderTimeout: readHeaderTimeout,
		IdleTimeout:       idleTimeout,
		BaseContext: func(net.Listener) context.Context {
			return ctx
		},
	}
	serverResult := make(chan error, 1)
	go func() {
		serverResult <- server.Serve(listener)
	}()
	if ready != nil {
		ready(launchURL)
	}

	var serveErr error
	select {
	case <-ctx.Done():
	case err := <-serverResult:
		if !errors.Is(err, http.ErrServerClosed) {
			serveErr = fmt.Errorf("serve release UI: %w", err)
		}
	}

	shutdownCtx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
	defer cancel()
	if err := server.Shutdown(shutdownCtx); err != nil && serveErr == nil {
		return fmt.Errorf("shut down release UI: %w", err)
	}
	return serveErr
}
