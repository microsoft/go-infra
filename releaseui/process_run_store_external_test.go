// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package releaseui_test

import (
	"context"

	"github.com/microsoft/go-infra/releaseui"
	"github.com/microsoft/go-infra/releaseui/contract"
)

type externalStore struct{}

func (externalStore) Create(
	context.Context,
	*releaseui.ReleaseRunState,
	*contract.RunView,
	*contract.Plan,
) (*releaseui.ReleaseRunRecord, error) {
	return nil, nil
}

func (externalStore) Get(context.Context, int) (*releaseui.ReleaseRunRecord, error) {
	return nil, nil
}

func (externalStore) Update(
	context.Context,
	*releaseui.ReleaseRunRecord,
	*releaseui.ReleaseRunState,
	*contract.RunView,
	*contract.Plan,
) (*releaseui.ReleaseRunRecord, error) {
	return nil, nil
}

func (externalStore) Query(context.Context, bool, int) ([]*releaseui.ReleaseRunRecord, error) {
	return nil, nil
}

func externalStatus(run *releaseui.ReleaseRunState) (releaseui.ReleaseRunStatus, error) {
	return run.Status()
}

var (
	_ releaseui.ReleaseRunStore = externalStore{}
	_                           = externalStatus
)
