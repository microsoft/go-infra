// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package runstore

import (
	"context"
	"errors"
	"time"

	"github.com/microsoft/go-infra/releaseui/contract"
)

// ErrConflict reports that a stored release changed before an update completed.
var ErrConflict = errors.New("release run revision changed")

// Store persists and discovers confirmed release runs.
//
// Implementations must use revision checks in Update and return [ErrConflict] when the record
// changed after the caller read it. Implementations must not retain or mutate arguments.
type Store interface {
	// Create stores run before releaseui starts external execution. view and plan contain derived
	// display data for the same run and may be nil.
	Create(ctx context.Context, run *State, view *contract.RunView, plan *contract.Plan) (*Record, error)

	// Get returns the record identified by id.
	Get(ctx context.Context, id int) (*Record, error)

	// Update replaces current with run when current.Revision still matches storage. view and plan
	// contain derived display data for run and may be nil.
	Update(ctx context.Context, current *Record, run *State, view *contract.RunView, plan *contract.Plan) (*Record, error)

	// Query returns at most limit records whose closed state equals closed, newest first.
	Query(ctx context.Context, closed bool, limit int) ([]*Record, error)
}

// Record is one revisioned release run returned by a Store.
type Record struct {
	ID        int
	Revision  int
	URL       string
	Closed    bool
	UpdatedAt time.Time
	Run       *State
}

// ValidateRecord checks the identity, lifecycle, and timestamps of a stored release.
func ValidateRecord(record *Record) error {
	if record == nil || record.ID <= 0 || record.Revision <= 0 || record.Run == nil {
		return errors.New("release run record is invalid")
	}
	if err := record.Run.Validate(); err != nil {
		return err
	}
	if record.Closed && !record.Run.Complete {
		return errors.New("closed release run record is incomplete")
	}
	if record.UpdatedAt.IsZero() {
		return errors.New("release run record update time is zero")
	}
	return nil
}
