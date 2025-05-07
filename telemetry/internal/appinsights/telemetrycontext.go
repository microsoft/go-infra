// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package appinsights

import (
	"strings"
	"time"

	"github.com/microsoft/go-infra/telemetry/internal/appinsights/internal/contracts"
)

// telemetryContext encapsulates contextual data common to all telemetry submitted through a
// TelemetryClient instance such as including instrumentation key, tags, and
// common properties.
type telemetryContext struct {
	// Instrumentation key
	iKey string

	// Stripped-down instrumentation key used in envelope name
	nameIKey string

	// Collection of tag data to attach to the telemetry item.
	Tags contracts.ContextTags
}

// newTelemetryContext creates a new, empty telemetryContext.
func newTelemetryContext(ikey string) *telemetryContext {
	return &telemetryContext{
		iKey:     ikey,
		nameIKey: "Microsoft.ApplicationInsights." + strings.Replace(ikey, "-", "", -1) + ".Event",
		Tags:     make(contracts.ContextTags),
	}
}

// Wraps a telemetry item in an envelope with the information found in this
// context.
func (context *telemetryContext) envelop(data contracts.EventData) *contracts.Envelope {
	envelope := contracts.NewEnvelope()
	envelope.Name = context.nameIKey
	envelope.Data = contracts.Data{
		BaseType: "EventData",
		BaseData: data,
	}
	envelope.IKey = context.iKey
	envelope.Time = time.Now().UTC()
	envelope.Tags = context.Tags
	return envelope
}
