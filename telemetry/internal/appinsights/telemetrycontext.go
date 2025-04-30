package appinsights

import (
	"log"
	"strings"

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
		nameIKey: strings.Replace(ikey, "-", "", -1),
		Tags:     make(contracts.ContextTags),
	}
}

// instrumentationKey gets the instrumentation key associated with this TelemetryContext.
// This will be an empty string on telemetry items' context instances.
func (context *telemetryContext) instrumentationKey() string {
	return context.iKey
}

// Wraps a telemetry item in an envelope with the information found in this
// context.
func (context *telemetryContext) envelop(item telemetry) *contracts.Envelope {
	tdata := item.telemetryData()
	data := contracts.NewData()
	data.BaseType = tdata.BaseType()
	data.BaseData = tdata

	envelope := contracts.NewEnvelope()
	envelope.Name = tdata.EnvelopeName(context.nameIKey)
	envelope.Data = data
	envelope.IKey = context.iKey

	timestamp := item.time()
	if timestamp.IsZero() {
		timestamp = now()
	}

	envelope.Time = timestamp.UTC().Format("2006-01-02T15:04:05.999999Z")

	// Create new tags object
	envelope.Tags = make(map[string]string)
	for k, v := range context.Tags {
		envelope.Tags[k] = v
	}

	// Create operation ID if it does not exist
	if _, ok := envelope.Tags[contracts.OperationId]; !ok {
		envelope.Tags[contracts.OperationId] = newUUID()
	}

	// Sanitize.
	for _, warn := range tdata.Sanitize() {
		log.Printf("Telemetry data warning: %s", warn)
	}
	for _, warn := range contracts.SanitizeTags(envelope.Tags) {
		log.Printf("Telemetry tag warning: %s", warn)
	}

	return envelope
}
