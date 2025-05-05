package appinsights

import (
	"bytes"
	"log"
	"strings"
	"testing"

	"github.com/microsoft/go-infra/telemetry/internal/appinsights/internal/contracts"
)

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

	// Set up listener for the warnings.
	oldOutput := log.Writer()
	defer log.SetOutput(oldOutput)

	var buf bytes.Buffer
	log.SetOutput(&buf)

	// This may break due to hardcoded limits... Check contracts.
	envelope := ctx.envelop(data)

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

	evdata := envelope.Data.BaseData
	if evdata.Name != name[:512] {
		t.Error("Event name was not truncated")
	}
}
