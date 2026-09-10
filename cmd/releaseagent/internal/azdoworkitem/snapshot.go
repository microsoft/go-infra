// Package azdoworkitem stores release state in Azure DevOps work items.
package azdoworkitem

import (
	"bytes"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"io"
	"net/url"
	"regexp"
	"strings"
)

const (
	CurrentSchemaVersion = 1
	MaxSnapshotSize      = 512 << 10
	MaxDescriptionSize   = 1_000_000
	descriptionMarker    = "releaseagent-state-v1:"
	maxDescriptionFields = 16
)

var processIDPattern = regexp.MustCompile(`^[a-z][a-z0-9-]*$`)

type Status string

const (
	StatusStarting  Status = "starting"
	StatusRunning   Status = "running"
	StatusSucceeded Status = "succeeded"
	StatusFailed    Status = "failed"
	StatusCanceled  Status = "canceled"
	StatusUncertain Status = "uncertain"
)

// Snapshot is the process-neutral envelope stored in a release work item.
// Process-specific validation remains with the owning release process.
type Snapshot struct {
	SchemaVersion int                 `json:"schemaVersion"`
	ProcessID     string              `json:"processId"`
	Status        Status              `json:"status"`
	Test          bool                `json:"test,omitempty"`
	IntentDigest  string              `json:"intentDigest"`
	Payload       json.RawMessage     `json:"payload"`
	Description   *DescriptionSummary `json:"-"`
}

// DescriptionSummary contains derived display data for the work item Description field.
// It is validated but deliberately excluded from the authoritative snapshot JSON.
type DescriptionSummary struct {
	ProcessName string
	Fields      []DescriptionField
}

// DescriptionField is one human-readable value, optionally linked to its source.
type DescriptionField struct {
	Label string
	Value string
	URL   string
}

func (s *Snapshot) Validate() error {
	if s == nil {
		return errors.New("release snapshot is nil")
	}
	if s.SchemaVersion != CurrentSchemaVersion {
		return fmt.Errorf("unsupported release snapshot schema %d", s.SchemaVersion)
	}
	if !processIDPattern.MatchString(s.ProcessID) {
		return fmt.Errorf("invalid release process ID %q", s.ProcessID)
	}
	switch s.Status {
	case StatusStarting, StatusRunning, StatusSucceeded, StatusFailed, StatusCanceled, StatusUncertain:
	default:
		return fmt.Errorf("invalid release status %q", s.Status)
	}
	if len(s.IntentDigest) != 64 {
		return errors.New("release intent digest must be a 64-character SHA-256 digest")
	}
	if _, err := hex.DecodeString(s.IntentDigest); err != nil {
		return errors.New("release intent digest must be hexadecimal")
	}
	if err := s.Description.validate(); err != nil {
		return err
	}
	var payload map[string]json.RawMessage
	if err := json.Unmarshal(s.Payload, &payload); err != nil || payload == nil {
		return errors.New("release payload must be a JSON object")
	}
	data, err := json.Marshal(s)
	if err != nil {
		return fmt.Errorf("marshal release snapshot: %w", err)
	}
	if len(data) > MaxSnapshotSize {
		return fmt.Errorf("release snapshot exceeds %d bytes", MaxSnapshotSize)
	}
	return nil
}

func (summary *DescriptionSummary) validate() error {
	if summary == nil {
		return nil
	}
	if name := strings.TrimSpace(summary.ProcessName); name == "" || len(name) > 100 {
		return errors.New("release description process name is invalid")
	}
	if len(summary.Fields) > maxDescriptionFields {
		return fmt.Errorf("release description has more than %d fields", maxDescriptionFields)
	}
	for _, field := range summary.Fields {
		label := strings.TrimSpace(field.Label)
		value := strings.TrimSpace(field.Value)
		if label == "" || len(label) > 100 || value == "" || len(value) > 2048 {
			return errors.New("release description field is invalid")
		}
		if field.URL == "" {
			continue
		}
		parsed, err := url.Parse(field.URL)
		if err != nil || parsed.Scheme != "https" || parsed.Host == "" || parsed.User != nil || len(field.URL) > 2048 {
			return fmt.Errorf("release description field %q has an invalid URL", label)
		}
	}
	return nil
}

// MarshalSnapshot validates and returns the canonical JSON representation of a snapshot.
func MarshalSnapshot(snapshot *Snapshot) ([]byte, error) {
	if err := snapshot.Validate(); err != nil {
		return nil, err
	}
	data, err := json.Marshal(snapshot)
	if err != nil {
		return nil, fmt.Errorf("marshal release snapshot: %w", err)
	}
	return data, nil
}

// ParseSnapshot strictly parses and validates a snapshot.
func ParseSnapshot(data []byte) (*Snapshot, error) {
	if len(data) > MaxSnapshotSize {
		return nil, fmt.Errorf("release snapshot exceeds %d bytes", MaxSnapshotSize)
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	var snapshot Snapshot
	if err := decoder.Decode(&snapshot); err != nil {
		return nil, fmt.Errorf("decode release snapshot: %w", err)
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return nil, errors.New("decode release snapshot: trailing JSON content")
	}
	if err := snapshot.Validate(); err != nil {
		return nil, err
	}
	return &snapshot, nil
}

// RenderDescription encodes a snapshot for the Azure DevOps HTML Description field.
func RenderDescription(snapshot *Snapshot) (string, error) {
	data, err := MarshalSnapshot(snapshot)
	if err != nil {
		return "", err
	}
	encoded := base64.RawURLEncoding.EncodeToString(data)
	releaseType := "Release"
	if snapshot.Test {
		releaseType = "Test / dry run"
	}
	processName := snapshot.ProcessID
	if snapshot.Description != nil {
		processName = snapshot.Description.ProcessName
	}
	var description strings.Builder
	description.WriteString("<h2>Release status</h2><table><tbody>")
	writeDescriptionField(&description, DescriptionField{Label: "Process", Value: processName})
	writeDescriptionField(&description, DescriptionField{Label: "Status", Value: statusLabel(snapshot.Status)})
	writeDescriptionField(&description, DescriptionField{Label: "Type", Value: releaseType})
	if snapshot.Description != nil {
		for _, field := range snapshot.Description.Fields {
			writeDescriptionField(&description, field)
		}
	}
	description.WriteString("</tbody></table>")
	description.WriteString("<p>Releaseagent manages this state. Add operator notes as comments.</p>")
	description.WriteString("<details><summary>Managed state</summary><p>Do not edit this value directly.</p><pre>")
	description.WriteString(descriptionMarker)
	description.WriteString(encoded)
	description.WriteString("</pre></details>")
	result := description.String()
	if len(result) > MaxDescriptionSize {
		return "", fmt.Errorf("release description exceeds %d characters", MaxDescriptionSize)
	}
	return result, nil
}

func writeDescriptionField(description *strings.Builder, field DescriptionField) {
	description.WriteString("<tr><td><strong>")
	description.WriteString(html.EscapeString(strings.TrimSpace(field.Label)))
	description.WriteString("</strong></td><td>")
	value := html.EscapeString(strings.TrimSpace(field.Value))
	if field.URL != "" {
		description.WriteString(`<a href="`)
		description.WriteString(html.EscapeString(field.URL))
		description.WriteString(`">`)
		description.WriteString(value)
		description.WriteString("</a>")
	} else {
		description.WriteString(value)
	}
	description.WriteString("</td></tr>")
}

func statusLabel(status Status) string {
	switch status {
	case StatusStarting:
		return "Starting"
	case StatusRunning:
		return "In progress"
	case StatusSucceeded:
		return "Succeeded"
	case StatusFailed:
		return "Failed"
	case StatusCanceled:
		return "Canceled"
	case StatusUncertain:
		return "Needs attention"
	default:
		return string(status)
	}
}

// ParseDescription decodes the releaseagent snapshot embedded in an HTML Description field.
func ParseDescription(description string) (*Snapshot, error) {
	if len(description) > MaxDescriptionSize {
		return nil, fmt.Errorf("release description exceeds %d characters", MaxDescriptionSize)
	}
	if strings.Count(description, descriptionMarker) != 1 {
		return nil, errors.New("release description must contain exactly one state marker")
	}
	remainder := description[strings.Index(description, descriptionMarker)+len(descriptionMarker):]
	end := 0
	for end < len(remainder) && isBase64URL(remainder[end]) {
		end++
	}
	if end == 0 {
		return nil, errors.New("release description state is empty")
	}
	data, err := base64.RawURLEncoding.DecodeString(remainder[:end])
	if err != nil {
		return nil, fmt.Errorf("decode release description state: %w", err)
	}
	return ParseSnapshot(data)
}

func isBase64URL(character byte) bool {
	return character >= 'A' && character <= 'Z' || character >= 'a' && character <= 'z' ||
		character >= '0' && character <= '9' || character == '-' || character == '_'
}
