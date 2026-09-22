// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package releaseui

import (
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/microsoft/go-infra/releaseui/contract"
)

const (
	resultSucceeded = "succeeded"
	resultFailed    = "failed"
	resultCanceled  = "canceled"
	resultUncertain = "uncertain"
)

// ReleaseRunState is releaseui-owned state wrapped around a process-owned snapshot.
type ReleaseRunState struct {
	ProcessID    string                  `json:"processId"`
	Snapshot     *contract.StateSnapshot `json:"snapshot"`
	Digest       string                  `json:"digest"`
	Started      bool                    `json:"started"`
	Checkpointed bool                    `json:"checkpointed,omitempty"`
	Complete     bool                    `json:"complete"`
	Result       string                  `json:"result,omitempty"`
	UpdatedAt    time.Time               `json:"-"`
}

func newProcessRunState(processID string, snapshot *contract.StateSnapshot) (*ReleaseRunState, error) {
	if err := validateStateSnapshot(snapshot); err != nil {
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
	return &ReleaseRunState{
		ProcessID: processID,
		Snapshot:  cloneStateSnapshot(snapshot),
		Digest:    fmt.Sprintf("%x", digest),
		UpdatedAt: time.Now().UTC(),
	}, nil
}

// Clone returns an independent copy of the release run state.
func (s *ReleaseRunState) Clone() *ReleaseRunState {
	if s == nil {
		return nil
	}
	clone := *s
	clone.Snapshot = cloneStateSnapshot(s.Snapshot)
	return &clone
}

// Validate checks the releaseui-owned state and its process snapshot.
func (s *ReleaseRunState) Validate() error {
	if s == nil {
		return errors.New("process run is nil")
	}
	if !processIDPattern.MatchString(s.ProcessID) {
		return fmt.Errorf("process run has invalid process ID %q", s.ProcessID)
	}
	if err := validateStateSnapshot(s.Snapshot); err != nil {
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
	if s.Complete && s.Result != resultSucceeded && s.Result != resultFailed &&
		s.Result != resultCanceled && s.Result != resultUncertain {

		return fmt.Errorf("completed process run has invalid result %q", s.Result)
	}
	return nil
}

func validateStateSnapshot(snapshot *contract.StateSnapshot) error {
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

func cloneStateSnapshot(snapshot *contract.StateSnapshot) *contract.StateSnapshot {
	if snapshot == nil {
		return nil
	}
	return &contract.StateSnapshot{
		Input: append(json.RawMessage(nil), snapshot.Input...),
		State: append(json.RawMessage(nil), snapshot.State...),
	}
}
