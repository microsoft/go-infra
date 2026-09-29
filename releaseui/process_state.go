// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package releaseui

import (
	"github.com/microsoft/go-infra/releaseui/contract"
	"github.com/microsoft/go-infra/releaseui/internal/runstore"
)

const (
	resultRunning   = string(runstore.StatusRunning)
	resultSucceeded = string(runstore.StatusSucceeded)
	resultFailed    = string(runstore.StatusFailed)
	resultCanceled  = string(runstore.StatusCanceled)
	resultUncertain = string(runstore.StatusUncertain)
)

type (
	// ReleaseRunStatus is the lifecycle status of a started release run.
	ReleaseRunStatus = runstore.Status

	// ReleaseRunState is releaseui-owned state wrapped around a process-owned snapshot.
	ReleaseRunState = runstore.State

	// ReleaseRunRecord is one revisioned release run returned by a ReleaseRunStore.
	ReleaseRunRecord = runstore.Record

	releaseRunState  = runstore.State
	releaseRunRecord = runstore.Record
)

const (
	ReleaseRunStatusRunning   = runstore.StatusRunning
	ReleaseRunStatusSucceeded = runstore.StatusSucceeded
	ReleaseRunStatusFailed    = runstore.StatusFailed
	ReleaseRunStatusCanceled  = runstore.StatusCanceled
	ReleaseRunStatusUncertain = runstore.StatusUncertain
)

// ErrReleaseRunConflict reports that a stored release changed before an update completed.
var ErrReleaseRunConflict = runstore.ErrConflict

var errReleaseRunConflict = ErrReleaseRunConflict

func validateReleaseRunRecord(record *releaseRunRecord) error {
	return runstore.ValidateRecord(record)
}

func validateStateSnapshot(snapshot *contract.StateSnapshot) error {
	return runstore.ValidateSnapshot(snapshot)
}

func cloneStateSnapshot(snapshot *contract.StateSnapshot) *contract.StateSnapshot {
	return runstore.CloneSnapshot(snapshot)
}
