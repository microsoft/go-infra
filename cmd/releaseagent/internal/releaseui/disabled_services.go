// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package releaseui

import (
	"context"
	"errors"
	"fmt"
	"strconv"

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

type importedRunMonitor struct {
	buildID int
	monitor func(context.Context, int) error
}

func (importedRunMonitor) PollAzDOMirror(context.Context, string, string, *releasesteps.Secret) error {
	return errors.New("an imported-run monitor cannot verify a source mirror")
}

func (m importedRunMonitor) TriggerBuildPipeline(context.Context, int, map[string]string, map[string]string, *releasesteps.Secret) (string, error) {
	return "", errors.New("an imported-run monitor cannot queue a pipeline")
}

func (m importedRunMonitor) PollPipelineComplete(ctx context.Context, buildID string, _ *releasesteps.Secret) error {
	id, err := strconv.Atoi(buildID)
	if err != nil || id != m.buildID {
		return fmt.Errorf("monitor build ID %q does not match imported build %d", buildID, m.buildID)
	}
	return m.monitor(ctx, id)
}

var _ releasesteps.GoImagesReleaseService = importedRunMonitor{}
