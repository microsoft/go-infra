package telemetry

import (
	"crypto/rand"
	"fmt"
	"runtime"

	"github.com/microsoft/go-infra/telemetry/internal/appinsights"
)

// Client is the global telemetry client used to send telemetry data.
//
// It is kept in an internal package to prevent direct access
// from outside the telemetry package.
var Client *appinsights.Client

// Init adds common tags to the telemetry client then assigns it to [Client].
func Init(client *appinsights.Client) {
	if client.Tags == nil {
		client.Tags = make(map[string]string)
	}

	// Generate a random session ID to uniquely identify this telemetry session.
	var sessionID [32]byte
	rand.Read(sessionID[:])

	// Add common tags to the client.
	client.Tags["ai.device.osVersion"] = runtime.GOOS + "/" + runtime.GOARCH
	client.Tags["ai.session.id"] = fmt.Sprintf("%x", sessionID[:])

	Client = client
}
