package appinsights

import (
	"strings"
	"testing"

	"github.com/microsoft/go-infra/telemetry/internal/appinsights/internal/contracts"
)

const test_ikey = "01234567-0000-89ab-cdef-000000000000"

func TestDefaultTags(t *testing.T) {
	context := newTelemetryContext(test_ikey)
	context.Tags["test"] = "OK"
	context.Tags["no-write"] = "Fail"

	data := contracts.EventData{
		Name: "Hello world.",
		Ver:  2,
	}

	envelope := context.envelop(data)

	if envelope.Tags["test"] != "OK" {
		t.Error("Default client tags did not propagate to telemetry")
	}

	if envelope.Tags["no-write"] != "Fail" {
		t.Error("Default client tag did not propagate to telemetry")
	}
}

func TestSanitize(t *testing.T) {
	name := strings.Repeat("Z", 1024)

	data := contracts.EventData{
		Name: name,
		Ver:  2,
	}

	ctx := newTelemetryContext(test_ikey)

	// We'll be looking for messages with these values:
	found := map[string]int{
		"EventData.Name exceeded": 0,
	}

	// This may break due to hardcoded limits... Check contracts.
	envelope := ctx.envelop(data)

	err := envelope.Sanitize()
	msg := err.Error()
	for k := range found {
		if strings.Contains(msg, k) {
			found[k] = found[k] + 1
		}
	}

	// Make sure all the warnings were found in the output
	for k, v := range found {
		if v != 1 {
			t.Errorf("Did not find a warning containing %q", k)
		}
	}

	evdata := envelope.Data.BaseData
	if evdata.Name != name[:512] {
		t.Error("Event name was not truncated")
	}
}

func TestSanitizeProperties(t *testing.T) {
	longKey := strings.Repeat("X", 300)
	longValue := strings.Repeat("Z", 9000)

	data := contracts.EventData{
		Name: "Test",
		Ver:  2,
		Properties: map[string]string{
			longKey: longValue,
			"ok":    "OK",
		},
	}

	ctx := newTelemetryContext(test_ikey)

	found := map[string]int{
		"EventData.Properties has value with length exceeding max": 0,
		"EventData.Properties has key with length exceeding max":   0,
	}

	envelope := ctx.envelop(data)

	err := envelope.Sanitize()
	msg := err.Error()
	for k := range found {
		if strings.Contains(msg, k) {
			found[k] = found[k] + 1
		}
	}

	for k, v := range found {
		if v != 1 {
			t.Errorf("Did not find a warning containing %q", k)
		}
	}

	ps := envelope.Data.BaseData.Properties
	if _, ok := ps[longKey]; ok {
		t.Error("Property with long key was not removed")
	}
	if v, ok := ps[longKey[:150]]; ok {
		if v != longValue[:8192] {
			t.Error("Property value was not truncated")
		}
	} else {
		t.Error("Property with truncated key was not added")
	}
	if ps["ok"] != "OK" {
		t.Error("Property with short key was changed")
	}
}
