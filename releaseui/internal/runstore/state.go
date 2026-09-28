// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

// Package runstore defines the private durable-state contract shared by releaseui and its store.
package runstore

import (
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"time"

	"github.com/microsoft/go-infra/releaseui/contract"
)

// Status is the lifecycle status of a started release run.
type Status string

const (
	StatusRunning   Status = "running"
	StatusSucceeded Status = "succeeded"
	StatusFailed    Status = "failed"
	StatusCanceled  Status = "canceled"
	StatusUncertain Status = "uncertain"
)

var processIDPattern = regexp.MustCompile(`^[a-z0-9]+(?:-[a-z0-9]+)*$`)

// State is releaseui-owned state wrapped around a process-owned snapshot.
type State struct {
	ProcessID    string                  `json:"processId"`
	Snapshot     *contract.StateSnapshot `json:"snapshot"`
	Digest       string                  `json:"digest"`
	Started      bool                    `json:"started"`
	Checkpointed bool                    `json:"checkpointed,omitempty"`
	Complete     bool                    `json:"complete"`
	Result       string                  `json:"result,omitempty"`
	UpdatedAt    time.Time               `json:"-"`
}

// NewState creates a release state with an immutable intent digest.
func NewState(processID string, snapshot *contract.StateSnapshot, now time.Time) (*State, error) {
	if err := ValidateSnapshot(snapshot); err != nil {
		return nil, err
	}
	digestSource, err := json.Marshal(struct {
		ProcessID string
		Snapshot  *contract.StateSnapshot
	}{ProcessID: processID, Snapshot: snapshot})
	if err != nil {
		return nil, fmt.Errorf("encode release intent: %w", err)
	}
	digest := sha256.Sum256(digestSource)
	return &State{
		ProcessID: processID,
		Snapshot:  CloneSnapshot(snapshot),
		Digest:    fmt.Sprintf("%x", digest),
		UpdatedAt: now,
	}, nil
}

// Clone returns an independent copy of the release state.
func (s *State) Clone() *State {
	if s == nil {
		return nil
	}
	clone := *s
	clone.Snapshot = CloneSnapshot(s.Snapshot)
	return &clone
}

// Validate checks releaseui-owned state and its process snapshot.
func (s *State) Validate() error {
	if s == nil {
		return errors.New("process run is nil")
	}
	if !processIDPattern.MatchString(s.ProcessID) {
		return fmt.Errorf("process run has invalid process ID %q", s.ProcessID)
	}
	if err := ValidateSnapshot(s.Snapshot); err != nil {
		return err
	}
	if s.Digest == "" {
		return errors.New("process run digest is empty")
	}
	if s.Checkpointed && !s.Started {
		return errors.New("process run checkpointed before it started")
	}
	if s.Complete && !s.Started {
		return errors.New("process run completed before it started")
	}
	if !s.Complete && s.Result != "" {
		return errors.New("incomplete process run has a result")
	}
	if s.Complete && s.Result != string(StatusSucceeded) && s.Result != string(StatusFailed) &&
		s.Result != string(StatusCanceled) && s.Result != string(StatusUncertain) {

		return fmt.Errorf("completed process run has invalid result %q", s.Result)
	}
	return nil
}

// Status returns the lifecycle status persisted by a release store.
func (s *State) Status() (Status, error) {
	if err := s.Validate(); err != nil {
		return "", err
	}
	if !s.Started {
		return "", errors.New("process run has not started")
	}
	if !s.Complete {
		return StatusRunning, nil
	}
	return Status(s.Result), nil
}

// ValidateSnapshot checks the process-owned JSON state.
func ValidateSnapshot(snapshot *contract.StateSnapshot) error {
	if snapshot == nil {
		return errors.New("process snapshot is nil")
	}
	if !json.Valid(snapshot.Input) {
		return errors.New("process snapshot input is invalid JSON")
	}
	if !json.Valid(snapshot.State) {
		return errors.New("process snapshot state is invalid JSON")
	}
	return nil
}

// CloneSnapshot returns an independent copy of process-owned state.
func CloneSnapshot(snapshot *contract.StateSnapshot) *contract.StateSnapshot {
	if snapshot == nil {
		return nil
	}
	return &contract.StateSnapshot{
		Input: append(json.RawMessage(nil), snapshot.Input...),
		State: append(json.RawMessage(nil), snapshot.State...),
	}
}
