// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

// Package releaseui runs the local browser UI for planning and observing releases.
package releaseui

import (
	"context"
	"fmt"
	"log"

	"github.com/microsoft/go-infra/releaseui/contract"
	"github.com/microsoft/go-infra/releaseui/internal/browser"
	"github.com/microsoft/go-infra/releaseui/internal/server"
	"github.com/microsoft/go-infra/releaseui/runstore"
)

type config struct {
	style       Style
	processes   []contract.ProcessGroup
	runStore    runstore.Store
	openBrowser bool
}

// Option configures the release UI.
type Option func(*config)

// Style configures the release UI brand and documentation link.
type Style struct {
	// Brand appears in the release UI title and header.
	Brand string

	// InstructionsURL is an optional link to release instructions shown in the top bar.
	InstructionsURL string
}

// WithStyle sets the release UI brand and optional instructions link.
func WithStyle(style Style) Option {
	return func(config *config) {
		config.style = style
	}
}

// WithProcesses sets the release process groups shown in the UI.
func WithProcesses(processes ...contract.ProcessGroup) Option {
	return func(config *config) {
		config.processes = append([]contract.ProcessGroup(nil), processes...)
	}
}

// WithRunStore replaces the built-in Azure-backed release store.
func WithRunStore(store runstore.Store) Option {
	return func(config *config) {
		config.runStore = store
	}
}

// WithBrowser opens the release UI in the operating system's default browser after the listener
// starts. A browser launch failure is logged and does not stop the server.
func WithBrowser() Option {
	return func(config *config) {
		config.openBrowser = true
	}
}

// ListenAndServe runs the release UI on a loopback address until ctx is canceled or the HTTP
// server stops.
func ListenAndServe(ctx context.Context, address string, options ...Option) error {
	var config config
	for _, option := range options {
		option(&config)
	}
	serverOptions := []server.Option{server.WithProcesses(config.processes...)}
	if config.style.Brand != "" {
		serverOptions = append(serverOptions, server.WithBrand(config.style.Brand))
	}
	if config.style.InstructionsURL != "" {
		serverOptions = append(serverOptions, server.WithInstructionsURL(config.style.InstructionsURL))
	}
	if config.runStore != nil {
		serverOptions = append(serverOptions, server.WithReleaseRunStore(config.runStore))
	}
	ready := func(launchURL string) {
		fmt.Printf("Release UI listening at %s\n", launchURL)
		if config.openBrowser {
			if err := browser.Open(launchURL); err != nil {
				log.Printf("Unable to open browser automatically: %v", err)
			}
		}
	}
	return server.ListenAndServe(ctx, address, ready, serverOptions...)
}
