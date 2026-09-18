// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package goimages

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"time"
)

// CurrentSchemaVersion identifies the current Go-images document format.
const CurrentSchemaVersion = 1

// Document is the durable, non-secret domain state of one Go-images release.
//
// Credentials and derived graph metadata are excluded.
type Document struct {
	SchemaVersion   int       `json:"schemaVersion"`
	ID              string    `json:"id"`
	CreatedAt       time.Time `json:"createdAt"`
	UpdatedAt       time.Time `json:"updatedAt"`
	Input           Input     `json:"input"`
	State           State     `json:"state"`
	ExecutionDigest string    `json:"executionDigest"`
}

// NewDocument creates and validates a new document without storing derived graph metadata.
func NewDocument(input *Input, state *State, now time.Time) (*Document, error) {
	if input == nil {
		return nil, errors.New("session input is nil")
	}
	if state == nil {
		return nil, errors.New("session state is nil")
	}
	if now.IsZero() {
		return nil, errors.New("session creation time is zero")
	}
	idBytes := make([]byte, 18)
	if _, err := rand.Read(idBytes); err != nil {
		return nil, fmt.Errorf("generate session ID: %w", err)
	}
	inputCopy, err := cloneJSON(*input)
	if err != nil {
		return nil, fmt.Errorf("copy session input: %w", err)
	}
	stateCopy, err := cloneJSON(*state)
	if err != nil {
		return nil, fmt.Errorf("copy session state: %w", err)
	}
	now = now.UTC()
	document := &Document{
		SchemaVersion: CurrentSchemaVersion,
		ID:            base64.RawURLEncoding.EncodeToString(idBytes),
		CreatedAt:     now,
		UpdatedAt:     now,
		Input:         inputCopy,
		State:         stateCopy,
	}
	document.ExecutionDigest, err = executionDigest(document.Input)
	if err != nil {
		return nil, err
	}
	if err := document.Validate(); err != nil {
		return nil, err
	}
	return document, nil
}

// Validate checks the document schema and immutable execution identity.
func (d *Document) Validate() error {
	if d == nil {
		return errors.New("session document is nil")
	}
	if d.SchemaVersion != CurrentSchemaVersion {
		return fmt.Errorf("unsupported session schema version %d", d.SchemaVersion)
	}
	if d.ID == "" {
		return errors.New("session ID is empty")
	}
	if d.CreatedAt.IsZero() || d.UpdatedAt.IsZero() {
		return errors.New("session timestamps must be set")
	}
	if d.UpdatedAt.Before(d.CreatedAt) {
		return errors.New("session update time precedes creation time")
	}
	if len(d.Input.Versions) == 0 {
		return errors.New("session has no release versions")
	}
	if err := ValidateState(&d.Input, &d.State); err != nil {
		return fmt.Errorf("validate session state: %w", err)
	}

	digest, err := executionDigest(d.Input)
	if err != nil {
		return err
	}
	if d.ExecutionDigest != digest {
		return fmt.Errorf("session execution digest mismatch: stored %q, calculated %q", d.ExecutionDigest, digest)
	}
	return nil
}

// WithState returns a detached document containing the latest release domain state.
func (d *Document) WithState(state *State, now time.Time) (*Document, error) {
	if err := d.Validate(); err != nil {
		return nil, err
	}
	if state == nil {
		return nil, errors.New("updated release state is nil")
	}
	if now.IsZero() {
		return nil, errors.New("session update time is zero")
	}
	document, err := cloneJSON(*d)
	if err != nil {
		return nil, fmt.Errorf("copy session document: %w", err)
	}
	stateCopy, err := cloneJSON(*state)
	if err != nil {
		return nil, fmt.Errorf("copy updated release state: %w", err)
	}
	document.State = stateCopy
	document.UpdatedAt = now.UTC()
	if err := document.Validate(); err != nil {
		return nil, err
	}
	return &document, nil
}

func executionDigest(input Input) (string, error) {
	data, err := json.Marshal(input)
	if err != nil {
		return "", fmt.Errorf("marshal session execution identity: %w", err)
	}
	digest := sha256.Sum256(data)
	return hex.EncodeToString(digest[:]), nil
}

func cloneJSON[T any](value T) (T, error) {
	var clone T
	data, err := json.Marshal(value)
	if err != nil {
		return clone, err
	}
	if err := json.Unmarshal(data, &clone); err != nil {
		return clone, err
	}
	return clone, nil
}
