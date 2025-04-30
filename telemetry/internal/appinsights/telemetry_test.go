package appinsights

import (
	"log"
	"math"
	"testing"

	"github.com/microsoft/go-infra/telemetry/internal/appinsights/internal/contracts"
)

const float_precision = 1e-4

func checkDataContract(t *testing.T, property string, actual, expected interface{}) {
	if x, ok := actual.(float64); ok {
		if y, ok := expected.(float64); ok {
			if math.Abs(x-y) > float_precision {
				t.Errorf("Float property %s mismatched; got %f, want %f.\n", property, actual, expected)
			}

			return
		}
	}

	if actual != expected {
		t.Errorf("Property %s mismatched; got %v, want %v.\n", property, actual, expected)
	}
}

func checkNotNullOrEmpty(t *testing.T, property string, actual interface{}) {
	if actual == nil {
		t.Errorf("Property %s was expected not to be null.\n", property)
	} else if str, ok := actual.(string); ok && str == "" {
		t.Errorf("Property %s was expected not to be an empty string.\n", property)
	}
}

func TestMain(m *testing.M) {
	log.SetFlags(log.Lshortfile)
	m.Run()
}

func TestEventTelemetry(t *testing.T) {
	mockClock()
	defer resetClock()

	telem := NewEventTelemetry("~my event~")
	d := telem.telemetryData().(*contracts.EventData)

	checkDataContract(t, "Name", d.Name, "~my event~")
	checkDataContract(t, "Timestamp", telem.time(), now())

	telem2 := &EventTelemetry{
		Name: "~my-event~",
	}
	d2 := telem2.telemetryData().(*contracts.EventData)

	checkDataContract(t, "Name", d2.Name, "~my-event~")
}
