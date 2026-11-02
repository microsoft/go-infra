// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package runstore

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"time"

	"github.com/microsoft/go-infra/releaseui/contract"
	"github.com/microsoft/go-infra/releaseui/coordinator"
)

// Reader retrieves published display data. Its limited contract keeps observers from loading or
// changing process checkpoints when they open another application's release.
type Reader interface {
	GetView(context.Context, int) (*ViewRecord, error)
	// QueryViews returns at most limit records, newest first. Partial results may accompany
	// RecordErrors; readers must display the valid views and report the errors.
	QueryViews(ctx context.Context, closed bool, limit int) ([]*ViewRecord, error)
}

// ViewRecord adds storage metadata to a published view.
type ViewRecord struct {
	ID       int    `json:"id"`
	Revision int    `json:"revision"`
	URL      string `json:"url"`
	Title    string `json:"title"`
	Closed   bool   `json:"closed"`
	View     *View  `json:"view"`
}

// View is the runner's self-contained display snapshot. Recovery uses State instead.
type View struct {
	ProcessID string               `json:"processId"`
	Name      string               `json:"name"`
	Mark      string               `json:"mark"`
	Digest    string               `json:"digest"`
	Status    Status               `json:"status"`
	Executing bool                 `json:"executing"`
	Test      bool                 `json:"test"`
	Detail    string               `json:"detail,omitempty"`
	RunURL    string               `json:"runUrl,omitempty"`
	Plan      *contract.Plan       `json:"plan,omitempty"`
	Graph     coordinator.Snapshot `json:"graph"`
	UpdatedAt time.Time            `json:"updatedAt"`
}

func (v *View) Validate() error {
	if v == nil || v.ProcessID == "" || strings.TrimSpace(v.Name) == "" || v.Digest == "" || v.UpdatedAt.IsZero() {
		return errors.New("published release view is incomplete")
	}
	switch v.Status {
	case StatusRunning, StatusSucceeded, StatusFailed, StatusCanceled, StatusUncertain:
	default:
		return fmt.Errorf("published release view has invalid status %q", v.Status)
	}
	if v.Executing && v.Status != StatusRunning {
		return errors.New("completed release view cannot claim an executing runner")
	}
	if len(v.Graph.Steps) == 0 || v.Graph.Active {
		return errors.New("published release view must contain an inactive graph snapshot")
	}
	if err := v.Graph.Validate(); err != nil {
		return err
	}
	if v.RunURL != "" {
		link, err := url.Parse(v.RunURL)
		if err != nil || link.Scheme != "https" || link.Host == "" || link.User != nil {
			return errors.New("published release view has an invalid external link")
		}
	}
	return nil
}

func (v *View) Clone() *View {
	if v == nil {
		return nil
	}
	clone := *v
	clone.Graph = v.Graph.Clone()
	if v.Plan != nil {
		plan := *v.Plan
		plan.Facts = append([]contract.PlanFact(nil), v.Plan.Facts...)
		clone.Plan = &plan
	}
	return &clone
}

func ValidateViewRecord(record *ViewRecord) error {
	if record == nil || record.ID <= 0 || record.Revision <= 0 {
		return errors.New("published release record identity is invalid")
	}
	return record.View.Validate()
}
