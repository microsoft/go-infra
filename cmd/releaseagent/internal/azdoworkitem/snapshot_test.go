package azdoworkitem

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
)

const testDigest = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"

func TestSnapshotRoundTrip(t *testing.T) {
	want := &Snapshot{
		SchemaVersion: CurrentSchemaVersion,
		ProcessID:     "go-images",
		Status:        StatusRunning,
		Test:          true,
		IntentDigest:  testDigest,
		Payload:       json.RawMessage(`{"buildId":"42"}`),
	}
	data, err := MarshalSnapshot(want)
	if err != nil {
		t.Fatal(err)
	}
	got, err := ParseSnapshot(data)
	if err != nil {
		t.Fatal(err)
	}
	if got.SchemaVersion != want.SchemaVersion || got.ProcessID != want.ProcessID ||
		got.Status != want.Status || got.Test != want.Test || got.IntentDigest != want.IntentDigest ||
		!bytes.Equal(got.Payload, want.Payload) {

		t.Fatalf("snapshot = %#v, want %#v", got, want)
	}
}

func TestSnapshotRejectsInvalidEnvelope(t *testing.T) {
	valid := `{"schemaVersion":1,"processId":"go-images","status":"running",` +
		`"intentDigest":"` + testDigest + `","payload":{"buildId":"42"}}`
	for _, test := range []struct {
		name string
		data string
	}{
		{name: "unknown field", data: strings.TrimSuffix(valid, "}") + `,"extra":true}`},
		{name: "unknown schema", data: strings.Replace(valid, `"schemaVersion":1`, `"schemaVersion":2`, 1)},
		{name: "invalid process", data: strings.Replace(valid, `"go-images"`, `"Go Images"`, 1)},
		{name: "invalid status", data: strings.Replace(valid, `"running"`, `"waiting"`, 1)},
		{name: "invalid digest", data: strings.Replace(valid, testDigest, strings.Repeat("z", 64), 1)},
		{name: "non-object payload", data: strings.Replace(valid, `{"buildId":"42"}`, `[]`, 1)},
		{name: "trailing JSON", data: valid + `{}`},
	} {
		t.Run(test.name, func(t *testing.T) {
			if _, err := ParseSnapshot([]byte(test.data)); err == nil {
				t.Fatal("invalid snapshot unexpectedly parsed")
			}
		})
	}
}

func TestSnapshotRejectsOversizedPayload(t *testing.T) {
	snapshot := &Snapshot{
		SchemaVersion: CurrentSchemaVersion,
		ProcessID:     "go-images",
		Status:        StatusStarting,
		IntentDigest:  testDigest,
		Payload:       json.RawMessage(`{"value":"` + strings.Repeat("x", MaxSnapshotSize) + `"}`),
	}
	if _, err := MarshalSnapshot(snapshot); err == nil {
		t.Fatal("oversized snapshot unexpectedly marshaled")
	}
}

func TestDescriptionRoundTrip(t *testing.T) {
	want := &Snapshot{
		SchemaVersion: CurrentSchemaVersion,
		ProcessID:     "go-images",
		Status:        StatusRunning,
		IntentDigest:  testDigest,
		Payload:       json.RawMessage(`{"buildId":"42","html":"<unsafe>"}`),
		Description: &DescriptionSummary{
			ProcessName: "Go images <release>",
			Fields: []DescriptionField{
				{Label: "Versions", Value: "1.26.8-1, 1.27.1-2"},
				{Label: "Azure build", Value: "3072236 & details", URL: "https://example.invalid/build?id=3072236&view=results"},
			},
		},
	}
	description, err := RenderDescription(want)
	if err != nil {
		t.Fatal(err)
	}
	for _, text := range []string{
		"<strong>Process</strong>", "Go images &lt;release&gt;",
		"<strong>Status</strong>", "In progress",
		"<strong>Type</strong>", "Release", "<summary>Managed state</summary>",
		"1.26.8-1, 1.27.1-2", `href="https://example.invalid/build?id=3072236&amp;view=results"`,
		"3072236 &amp; details",
	} {
		if !strings.Contains(description, text) {
			t.Fatalf("description does not contain %q: %q", text, description)
		}
	}
	if strings.Contains(description, `{"buildId"`) || !strings.Contains(description, descriptionMarker) {
		t.Fatalf("description does not use encoded transport: %q", description)
	}
	got, err := ParseDescription("<div>" + description + "</div>")
	if err != nil {
		t.Fatal(err)
	}
	wantJSON, err := MarshalSnapshot(want)
	if err != nil {
		t.Fatal(err)
	}
	gotJSON, err := MarshalSnapshot(got)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(gotJSON, wantJSON) {
		t.Fatalf("snapshot = %s, want %s", gotJSON, wantJSON)
	}
	if got.Description != nil {
		t.Fatalf("parsed snapshot retained non-authoritative description: %#v", got.Description)
	}
}

func TestDescriptionRejectsInvalidSummary(t *testing.T) {
	for _, field := range []DescriptionField{
		{Label: "", Value: "value"},
		{Label: "Target", Value: "value", URL: "http://example.invalid"},
		{Label: "Target", Value: "value", URL: "https://user@example.invalid"},
	} {
		snapshot := &Snapshot{
			SchemaVersion: CurrentSchemaVersion,
			ProcessID:     "go-images",
			Status:        StatusRunning,
			IntentDigest:  testDigest,
			Payload:       json.RawMessage(`{"buildId":"42"}`),
			Description:   &DescriptionSummary{ProcessName: "Go images", Fields: []DescriptionField{field}},
		}
		if _, err := RenderDescription(snapshot); err == nil {
			t.Fatalf("invalid description field unexpectedly rendered: %#v", field)
		}
	}
}

func TestDescriptionLabelsTestRun(t *testing.T) {
	description, err := RenderDescription(&Snapshot{
		SchemaVersion: CurrentSchemaVersion,
		ProcessID:     "go-infra",
		Status:        StatusUncertain,
		Test:          true,
		IntentDigest:  testDigest,
		Payload:       json.RawMessage(`{"action":"manual-dispatch"}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(description, "Test / dry run") || !strings.Contains(description, "Needs attention") {
		t.Fatalf("description = %q", description)
	}
}

func TestDescriptionRejectsInvalidTransport(t *testing.T) {
	valid, err := RenderDescription(&Snapshot{
		SchemaVersion: CurrentSchemaVersion,
		ProcessID:     "go-images",
		Status:        StatusStarting,
		IntentDigest:  testDigest,
		Payload:       json.RawMessage(`{"buildId":"42"}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, description := range []string{
		"<p>no state</p>",
		valid + valid,
		"<pre>" + descriptionMarker + "!</pre>",
		"<pre>" + descriptionMarker + "not-valid-json</pre>",
	} {
		if _, err := ParseDescription(description); err == nil {
			t.Fatalf("invalid description unexpectedly parsed: %q", description)
		}
	}
}
