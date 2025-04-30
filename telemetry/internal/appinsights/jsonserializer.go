package appinsights

import (
	"bytes"
	"encoding/json"
	"log"

	"github.com/microsoft/go-infra/telemetry/internal/appinsights/internal/contracts"
)

type telemetryBufferItems []*contracts.Envelope

func (items telemetryBufferItems) serialize() []byte {
	var result bytes.Buffer
	encoder := json.NewEncoder(&result)

	for _, item := range items {
		end := result.Len()
		if err := encoder.Encode(item); err != nil {
			log.Printf("Telemetry item failed to serialize: %s", err.Error())
			result.Truncate(end)
		}
	}

	return result.Bytes()
}
