package appinsights

import (
	"time"

	"github.com/microsoft/go-infra/telemetry/internal/appinsights/internal/contracts"
)

// Common interface implemented by telemetry data contracts
type telemetryData interface {
	EnvelopeName(string) string
	BaseType() string
	Sanitize() []string
}

// Common interface implemented by telemetry items that can be passed to
// TelemetryClient.Track
type telemetry interface {
	// Gets the time when this item was measured
	time() time.Time

	// Gets the data contract as it will be submitted to the data
	// collector.
	telemetryData() telemetryData
}

// Event telemetry items represent structured event records.
type EventTelemetry struct {
	// The time this when this item was measured
	Timestamp time.Time

	// Event name
	Name string
}

// Creates an event telemetry item with the specified name.
func NewEventTelemetry(name string) *EventTelemetry {
	return &EventTelemetry{
		Name:      name,
		Timestamp: now(),
	}
}

func (event *EventTelemetry) time() time.Time {
	return event.Timestamp
}

func (event *EventTelemetry) telemetryData() telemetryData {
	data := contracts.NewEventData()
	data.Name = event.Name

	return data
}
