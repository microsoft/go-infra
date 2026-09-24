// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package releaseui

import (
	"context"
	"errors"
	"time"

	"github.com/microsoft/go-infra/releaseui/contract"
)

// ErrReleaseRunConflict reports that a stored release changed before an update completed.
var ErrReleaseRunConflict = errors.New("release run revision changed")

// ReleaseRunStore persists and discovers confirmed release runs.
type ReleaseRunStore interface {
	Create(context.Context, *ReleaseRunState, *contract.RunView, *contract.Plan) (*ReleaseRunRecord, error)
	Get(context.Context, int) (*ReleaseRunRecord, error)
	Update(context.Context, *ReleaseRunRecord, *ReleaseRunState, *contract.RunView, *contract.Plan) (*ReleaseRunRecord, error)
	Query(context.Context, bool, int) ([]*ReleaseRunRecord, error)
}

// ReleaseRunRecord is one revisioned release run returned by a ReleaseRunStore.
type ReleaseRunRecord struct {
	ID        int
	Revision  int
	URL       string
	Closed    bool
	UpdatedAt time.Time
	Run       *ReleaseRunState
}

func validateReleaseRunRecord(record *ReleaseRunRecord) error {
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
