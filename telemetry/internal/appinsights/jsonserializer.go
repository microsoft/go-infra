// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package appinsights

import (
	"bytes"
	"encoding/json"
	"fmt"

	"github.com/microsoft/go-infra/telemetry/internal/appinsights/internal/contracts"
)

func serialize(items []*contracts.Envelope) ([]byte, error) {
	var result bytes.Buffer
	encoder := json.NewEncoder(&result)

	var nfail int
	for _, item := range items {
		end := result.Len()
		if err := encoder.Encode(item); err != nil {
			nfail++
			result.Truncate(end)
		}
	}
	ret := result.Bytes()
	if nfail > 0 {
		if nfail == len(items) {
			ret = nil
		}
		return ret, fmt.Errorf("failed to serialize %d items out of %d", nfail, len(items))
	}

	return ret, nil
}
