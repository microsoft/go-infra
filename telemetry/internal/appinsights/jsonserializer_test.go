//go:build goexperiment.synctest

package appinsights

import (
	"bytes"
	"encoding/json"
	"fmt"
	"math"
	"strconv"
	"strings"
	"testing"
	"testing/synctest"

	"github.com/microsoft/go-infra/telemetry/internal/appinsights/internal/contracts"
)

func TestJsonSerializerEvents(t *testing.T) {
	synctest.Run(func() {
		var buffer []batchItem

		addEventData(&buffer, contracts.EventData{
			Name: "an-event",
			Ver:  2,
		},
		)
		v, err := serialize(buffer)
		if err != nil {
			t.Fatal(err)
		}
		j, err := parsePayload(v)
		if err != nil {
			t.Errorf("Error parsing payload: %s", err.Error())
		}

		if len(j) != 1 {
			t.Fatal("Unexpected event count")
		}

		// Event
		j[0].assertPath(t, "iKey", test_ikey)
		j[0].assertPath(t, "name", "Microsoft.ApplicationInsights.01234567000089abcdef000000000000.Event")
		j[0].assertPath(t, "time", "2000-01-01T00:00:00Z")
		j[0].assertPath(t, "sampleRate", 100.0)
		j[0].assertPath(t, "data.baseType", "EventData")
		j[0].assertPath(t, "data.baseData.name", "an-event")
		j[0].assertPath(t, "data.baseData.ver", 2)
	})
}

type jsonMessage map[string]any
type jsonPayload []jsonMessage

func parsePayload(payload []byte) (jsonPayload, error) {
	// json.Decoder can detect line endings for us but I'd like to explicitly find them.
	var result jsonPayload
	for _, item := range bytes.Split(payload, []byte("\n")) {
		if len(item) == 0 {
			continue
		}

		decoder := json.NewDecoder(bytes.NewReader(item))
		msg := make(jsonMessage)
		if err := decoder.Decode(&msg); err == nil {
			result = append(result, msg)
		} else {
			return result, err
		}
	}

	return result, nil
}

func (msg jsonMessage) assertPath(t *testing.T, path string, value any) {
	t.Helper()
	const tolerance = 0.0001
	v, err := msg.getPath(path)
	if err != nil {
		t.Error(err.Error())
		return
	}

	if num, ok := value.(int); ok {
		if vnum, ok := v.(float64); ok {
			if math.Abs(float64(num)-vnum) > tolerance {
				t.Errorf("Data was unexpected at %s. Got %g want %d", path, vnum, num)
			}
		} else if vnum, ok := v.(int); ok {
			if vnum != num {
				t.Errorf("Data was unexpected at %s. Got %d want %d", path, vnum, num)
			}
		} else {
			t.Errorf("Expected value at %s to be a number, but was %T", path, v)
		}
	} else if num, ok := value.(float64); ok {
		if vnum, ok := v.(float64); ok {
			if math.Abs(num-vnum) > tolerance {
				t.Errorf("Data was unexpected at %s. Got %g want %g", path, vnum, num)
			}
		} else if vnum, ok := v.(int); ok {
			if math.Abs(num-float64(vnum)) > tolerance {
				t.Errorf("Data was unexpected at %s. Got %d want %g", path, vnum, num)
			}
		} else {
			t.Errorf("Expected value at %s to be a number, but was %T", path, v)
		}
	} else if str, ok := value.(string); ok {
		if vstr, ok := v.(string); ok {
			if str != vstr {
				t.Errorf("Data was unexpected at %s. Got '%s' want '%s'", path, vstr, str)
			}
		} else {
			t.Errorf("Expected value at %s to be a string, but was %T", path, v)
		}
	} else if bl, ok := value.(bool); ok {
		if vbool, ok := v.(bool); ok {
			if bl != vbool {
				t.Errorf("Data was unexpected at %s. Got %t want %t", path, vbool, bl)
			}
		} else {
			t.Errorf("Expected value at %s to be a bool, but was %T", path, v)
		}
	} else {
		t.Errorf("Unsupported type: %#v", value)
	}
}

func (msg jsonMessage) getPath(path string) (any, error) {
	parts := strings.Split(path, ".")
	var obj any = msg
	for i, part := range parts {
		if strings.HasPrefix(part, "[") && strings.HasSuffix(part, "]") {
			// Array
			idxstr := part[1 : len(part)-2]
			idx, _ := strconv.Atoi(idxstr)

			if ar, ok := obj.([]any); ok {
				if idx >= len(ar) {
					return nil, fmt.Errorf("Index out of bounds: %s", strings.Join(parts[0:i+1], "."))
				}

				obj = ar[idx]
			} else {
				return nil, fmt.Errorf("Path %s is not an array", strings.Join(parts[0:i], "."))
			}
		} else if part == "<len>" {
			if ar, ok := obj.([]any); ok {
				return len(ar), nil
			}
		} else {
			// Map
			if dict, ok := obj.(jsonMessage); ok {
				if val, ok := dict[part]; ok {
					obj = val
				} else {
					return nil, fmt.Errorf("Key %s not found in %s", part, strings.Join(parts[0:i], "."))
				}
			} else if dict, ok := obj.(map[string]any); ok {
				if val, ok := dict[part]; ok {
					obj = val
				} else {
					return nil, fmt.Errorf("Key %s not found in %s", part, strings.Join(parts[0:i], "."))
				}
			} else {
				return nil, fmt.Errorf("Path %s is not a map", strings.Join(parts[0:i], "."))
			}
		}
	}

	return obj, nil
}
