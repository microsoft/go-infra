// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package contracts

import (
	"errors"
	"time"
)

// Data struct to contain both B and C sections.
type Data struct {
	BaseType string    `json:"baseType"`
	BaseData EventData `json:"baseData"`
}

// EventData represents structured event records that can be grouped
// and searched by their properties. Event data item also creates a metric of
// event count by name.
type EventData struct {
	// Schema version
	Ver int `json:"ver"`

	// Event name. Keep it low cardinality to allow proper grouping and useful
	// metrics.
	Name string `json:"name"`

	// Properties is a collection of custom properties for this specific event.
	Properties map[string]string `json:"properties,omitempty"`
}

// Envelope is the telemetry payload that is sent to the Application Insights.
type Envelope struct {
	// Envelope version. For internal use only. By assigning this the default, it
	// will not be serialized within the payload unless changed to a value other
	// than #1.
	Ver int `json:"ver"`

	// Type name of telemetry data item.
	Name string `json:"name"`

	// Event date time when telemetry item was created. This is the wall clock
	// time on the client when the event was generated. There is no guarantee that
	// the client's time is accurate. This field must be formatted in UTC ISO 8601
	// format, with a trailing 'Z' character, as described publicly on
	// https://en.wikipedia.org/wiki/ISO_8601#UTC. Note: the number of decimal
	// seconds digits provided are variable (and unspecified). Consumers should
	// handle this, i.e. managed code consumers should not use format 'O' for
	// parsing as it specifies a fixed length. Example:
	// 2009-06-15T13:45:30.0000000Z.
	Time time.Time `json:"time"`

	// Sampling rate used in application. This telemetry item represents 1 /
	// sampleRate actual telemetry items.
	SampleRate float64 `json:"sampleRate"`

	// Sequence field used to track absolute order of uploaded events.
	Seq string `json:"seq"`

	// The application's instrumentation key. The key is typically represented as
	// a GUID, but there are cases when it is not a guid. No code should rely on
	// iKey being a GUID. Instrumentation key is case insensitive.
	IKey string `json:"iKey"`

	// Key/value collection of context properties. See ContextTagKeys for
	// information on available properties.
	Tags map[string]string `json:"tags,omitempty"`

	// Telemetry data item.
	Data Data `json:"data"`
}

// Sanitize truncates string fields that exceed their maximum supported sizes for this
// object and all objects it references. Returns a warning for each affected
// field.
func (data *Envelope) Sanitize() error {
	var errs []error

	if len(data.Name) > 1024 {
		data.Name = data.Name[:1024]
		errs = append(errs, errors.New("Envelope.Name exceeded maximum length of 1024"))
	}

	if props := data.Data.BaseData.Properties; len(props) > 0 {
		for k, v := range props {
			if len(v) > 8192 {
				props[k] = v[:8192]
				errs = append(errs, errors.New("EventData.Properties has value with length exceeding max of 8192: "+k))
			}
			if len(k) > 150 {
				props[k[:150]] = props[k]
				delete(props, k)
				errs = append(errs, errors.New("EventData.Properties has key with length exceeding max of 150: "+k))
			}
		}
	}

	if len(data.Seq) > 64 {
		data.Seq = data.Seq[:64]
		errs = append(errs, errors.New("Envelope.Seq exceeded maximum length of 64"))
	}

	if len(data.IKey) > 40 {
		data.IKey = data.IKey[:40]
		errs = append(errs, errors.New("Envelope.IKey exceeded maximum length of 40"))
	}

	if len(data.Data.BaseData.Name) > 512 {
		data.Data.BaseData.Name = data.Data.BaseData.Name[:512]
		errs = append(errs, errors.New("EventData.Name exceeded maximum length of 512"))
	}

	return errors.Join(errs...)
}

// NewEnvelope creates a new [Envelope] instance with default values set by the schema.
func NewEnvelope() *Envelope {
	return &Envelope{
		Ver:        1,
		SampleRate: 100.0,
	}
}
