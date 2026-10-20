// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package runstore

import (
	"context"
	"errors"
	"fmt"
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
	// If individual records cannot be read, it may return valid records alongside an error
	// describing the failures. A RecordError preserves read-only metadata and recovery guidance.
	// Callers must handle both the records and the error.
	Query(ctx context.Context, closed bool, limit int) ([]*Record, error)
}

// Record is one revisioned release run returned by a Store.
type Record struct {
	ID        int
	Revision  int
	URL       string
	Title     string
	Closed    bool
	UpdatedAt time.Time
	Run       *State
}

// RecordError describes an unreadable record without exposing it as executable state.
type RecordError struct {
	ID        int
	URL       string
	Title     string
	UpdatedAt time.Time
	// Closed determines history grouping independently of the provider's display state.
	Closed bool
	// RecordState is optional provider metadata for display, not lifecycle decisions.
	RecordState string
	SavedStatus string
	Message     string
	Guidance    string
	Err         error
}

func (e *RecordError) Error() string {
	return fmt.Sprintf("release record %d (%s): %v", e.ID, e.URL, e.Err)
}

func (e *RecordError) Unwrap() error { return e.Err }

// ValidateRecord checks the identity, saved state, and timestamps of a stored release.
// Closing a tracking record does not change the last saved execution status.
func ValidateRecord(record *Record) error {
	if record == nil || record.ID <= 0 || record.Revision <= 0 || record.Run == nil {
		return errors.New("release run record is invalid")
	}
	if err := record.Run.Validate(); err != nil {
		return err
	}
	if record.UpdatedAt.IsZero() {
		return errors.New("release run record update time is zero")
	}
	return nil
}
