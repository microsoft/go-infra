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
type Store interface {
	Create(context.Context, *State, *contract.RunView, *contract.Plan) (*Record, error)
	Get(context.Context, int) (*Record, error)
	Update(context.Context, *Record, *State, *contract.RunView, *contract.Plan) (*Record, error)
	Query(context.Context, bool, int) ([]*Record, error)
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
