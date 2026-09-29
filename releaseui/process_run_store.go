// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package releaseui

import (
	"context"

	"github.com/microsoft/go-infra/releaseui/contract"
)

// ReleaseRunStore persists and discovers confirmed release runs.
//
// Implementations must use revision checks in Update and return [ErrReleaseRunConflict] when the
// record changed after the caller read it. Implementations must not retain or mutate arguments.
type ReleaseRunStore interface {
	// Create stores run before releaseui starts external execution. view and plan contain derived
	// display data for the same run and may be nil.
	Create(ctx context.Context, run *ReleaseRunState, view *contract.RunView, plan *contract.Plan) (*ReleaseRunRecord, error)

	// Get returns the record identified by id.
	Get(ctx context.Context, id int) (*ReleaseRunRecord, error)

	// Update replaces current with run when current.Revision still matches storage. view and plan
	// contain derived display data for run and may be nil.
	Update(ctx context.Context, current *ReleaseRunRecord, run *ReleaseRunState, view *contract.RunView, plan *contract.Plan) (*ReleaseRunRecord, error)

	// Query returns at most limit records whose closed state equals closed, newest first.
	Query(ctx context.Context, closed bool, limit int) ([]*ReleaseRunRecord, error)
}
