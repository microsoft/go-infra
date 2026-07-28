// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package releaseui

import (
	"context"
	"errors"

	"github.com/microsoft/go-infra/cmd/releaseagent/internal/releasesteps"
)

// ErrExternalExecutionDisabled is returned by every external service operation in this build.
var ErrExternalExecutionDisabled = errors.New("external release execution is disabled")

// disabledServices makes the safety boundary explicit. Planning can retain the real step
// functions, but accidentally executing one cannot contact GitHub, Azure DevOps, or a publisher.
type disabledServices struct{}

func (disabledServices) CreateReleaseDayTrackingIssue(context.Context, string, string, []string, *releasesteps.Secret) (int, error) {
	return 0, ErrExternalExecutionDisabled
}

func (disabledServices) PollUpstreamTagCommit(context.Context, string) (string, error) {
	return "", ErrExternalExecutionDisabled
}

func (disabledServices) CreateGitHubSyncPR(context.Context, string, string, *releasesteps.Secret) (int, error) {
	return 0, ErrExternalExecutionDisabled
}

func (disabledServices) PollMergedGitHubPRCommit(context.Context, string, int, *releasesteps.Secret) (string, error) {
	return "", ErrExternalExecutionDisabled
}

func (disabledServices) PollAzDOMirror(context.Context, string, string, *releasesteps.Secret) error {
	return ErrExternalExecutionDisabled
}

func (disabledServices) GetTargetBranch(context.Context, string) (string, error) {
	return "", ErrExternalExecutionDisabled
}

func (disabledServices) TriggerBuildPipeline(context.Context, int, map[string]string, map[string]string, *releasesteps.Secret) (string, error) {
	return "", ErrExternalExecutionDisabled
}

func (disabledServices) PollPipelineComplete(context.Context, string, *releasesteps.Secret) error {
	return ErrExternalExecutionDisabled
}

func (disabledServices) DownloadPipelineArtifactToDir(context.Context, string, string, *releasesteps.Secret) (string, error) {
	return "", ErrExternalExecutionDisabled
}

func (disabledServices) VerifyAssetVersion(context.Context, string, string) error {
	return ErrExternalExecutionDisabled
}

func (disabledServices) CreateGitHubTag(context.Context, string, string, string, string, *releasesteps.Secret) error {
	return ErrExternalExecutionDisabled
}

func (disabledServices) CreateGitHubRelease(context.Context, string, string, string, string, *releasesteps.Secret) error {
	return ErrExternalExecutionDisabled
}

func (disabledServices) CreateDockerImagesPR(context.Context, string, string, string, *releasesteps.Secret) (int, error) {
	return 0, ErrExternalExecutionDisabled
}

func (disabledServices) PollImagesCommit(context.Context, []string, *releasesteps.Secret) (string, error) {
	return "", ErrExternalExecutionDisabled
}

func (disabledServices) CheckLatestMARGoVersion(context.Context, []string) error {
	return ErrExternalExecutionDisabled
}

func (disabledServices) CreateAnnouncementBlogFile(context.Context, []string, string, bool, *releasesteps.Secret) error {
	return ErrExternalExecutionDisabled
}

var _ releasesteps.ServiceBundle = disabledServices{}
