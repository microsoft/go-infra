package appinsights

import (
	"bytes"
	"log"
	"strings"
	"testing"
	"time"

	"github.com/microsoft/go-infra/telemetry/internal/appinsights/internal/contracts"
)

func TestDefaultTags(t *testing.T) {
	context := newTelemetryContext(test_ikey)
	context.Tags["test"] = "OK"
	context.Tags["no-write"] = "Fail"

	telem := NewEventTelemetry("Hello world.")

	envelope := context.envelop(telem)

	if envelope.Tags["test"] != "OK" {
		t.Error("Default client tags did not propagate to telemetry")
	}

	if envelope.Tags["no-write"] != "Fail" {
		t.Error("Default client tag did not propagate to telemetry")
	}
}

func TestContextTags(t *testing.T) {
	// Just a quick test to make sure it works.
	tags := make(contracts.ContextTags)
	if v := tags.Session().GetId(); v != "" {
		t.Error("Failed to get empty session ID")
	}

	tags.Session().SetIsFirst("true")
	if v := tags.Session().GetIsFirst(); v != "true" {
		t.Error("Failed to get value")
	}

	if v, ok := tags["ai.session.isFirst"]; !ok || v != "true" {
		t.Error("Failed to get isFirst through raw map")
	}

	tags.Session().SetIsFirst("")
	if v, ok := tags["ai.session.isFirst"]; ok || v != "" {
		t.Error("SetIsFirst with empty string failed to remove it from the map")
	}
}

func TestSanitize(t *testing.T) {
	name := strings.Repeat("Z", 1024)

	ev := NewEventTelemetry(name)

	ctx := newTelemetryContext(test_ikey)
	ctx.Tags.Session().SetId(name)

	// We'll be looking for messages with these values:
	found := map[string]int{
		"EventData.Name exceeded": 0,
		"ai.session.id exceeded":  0,
	}

	// Set up listener for the warnings.
	oldOutput := log.Writer()
	defer log.SetOutput(oldOutput)

	var buf bytes.Buffer
	log.SetOutput(&buf)

	// This may break due to hardcoded limits... Check contracts.
	envelope := ctx.envelop(ev)

	out := buf.String()
	for k := range found {
		if strings.Contains(out, k) {
			found[k] = found[k] + 1
		}
	}

	// Make sure all the warnings were found in the output
	for k, v := range found {
		if v != 1 {
			t.Errorf("Did not find a warning containing \"%s\"", k)
		}
	}

	evdata := envelope.Data.(*contracts.Data).BaseData.(*contracts.EventData)
	if evdata.Name != name[:512] {
		t.Error("Event name was not truncated")
	}
}

func TestTimestamp(t *testing.T) {
	ev := NewEventTelemetry("event")
	ev.Timestamp = time.Unix(1523667421, 500000000)

	envelope := newTelemetryContext(test_ikey).envelop(ev)
	if envelope.Time != "2018-04-14T00:57:01.5Z" {
		t.Errorf("Unexpected timestamp: %s", envelope.Time)
	}
}
