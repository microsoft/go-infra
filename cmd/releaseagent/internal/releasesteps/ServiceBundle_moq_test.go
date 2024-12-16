// Code generated by moq; DO NOT EDIT.
// github.com/matryer/moq

package releasesteps

import (
	"context"
	"sync"
)

// Ensure, that ServiceBundleMock does implement ServiceBundle.
// If this is not the case, regenerate this file with moq.
var _ ServiceBundle = &ServiceBundleMock{}

// ServiceBundleMock is a mock implementation of ServiceBundle.
//
//	func TestSomethingThatUsesServiceBundle(t *testing.T) {
//
//		// make and configure a mocked ServiceBundle
//		mockedServiceBundle := &ServiceBundleMock{
//			CheckLatestMARGoVersionFunc: func(ctx context.Context, versions []string) error {
//				panic("mock out the CheckLatestMARGoVersion method")
//			},
//			CreateAnnouncementBlogFileFunc: func(ctx context.Context, versions []string, user string, security bool, secret *Secret) error {
//				panic("mock out the CreateAnnouncementBlogFile method")
//			},
//			CreateDockerImagesPRFunc: func(ctx context.Context, repo string, assetJSONPath string, manualBranch string, secret *Secret) (int, error) {
//				panic("mock out the CreateDockerImagesPR method")
//			},
//			CreateGitHubReleaseFunc: func(ctx context.Context, repo string, tag string, assetJSONPath string, buildAssetDir string, secret *Secret) error {
//				panic("mock out the CreateGitHubRelease method")
//			},
//			CreateGitHubSyncPRFunc: func(ctx context.Context, repo string, branch string, secret *Secret) (int, error) {
//				panic("mock out the CreateGitHubSyncPR method")
//			},
//			CreateGitHubTagFunc: func(ctx context.Context, version string, repo string, tag string, commit string, secret *Secret) error {
//				panic("mock out the CreateGitHubTag method")
//			},
//			CreateReleaseDayTrackingIssueFunc: func(ctx context.Context, repo string, runner string, versions []string, secret *Secret) (int, error) {
//				panic("mock out the CreateReleaseDayTrackingIssue method")
//			},
//			DownloadPipelineArtifactToDirFunc: func(ctx context.Context, buildID string, artifactName string, secret *Secret) (string, error) {
//				panic("mock out the DownloadPipelineArtifactToDir method")
//			},
//			GetTargetBranchFunc: func(ctx context.Context, version string) (string, error) {
//				panic("mock out the GetTargetBranch method")
//			},
//			PollAzDOMirrorFunc: func(ctx context.Context, target string, commit string, secret *Secret) error {
//				panic("mock out the PollAzDOMirror method")
//			},
//			PollImagesCommitFunc: func(ctx context.Context, versions []string, secret *Secret) (string, error) {
//				panic("mock out the PollImagesCommit method")
//			},
//			PollMergedGitHubPRCommitFunc: func(ctx context.Context, repo string, pr int, secret *Secret) (string, error) {
//				panic("mock out the PollMergedGitHubPRCommit method")
//			},
//			PollPipelineCompleteFunc: func(ctx context.Context, buildID string, secret *Secret) error {
//				panic("mock out the PollPipelineComplete method")
//			},
//			PollUpstreamTagCommitFunc: func(ctx context.Context, version string) (string, error) {
//				panic("mock out the PollUpstreamTagCommit method")
//			},
//			TriggerBuildPipelineFunc: func(ctx context.Context, pipelineID int, parameters map[string]string, optionalParameters map[string]string, secret *Secret) (string, error) {
//				panic("mock out the TriggerBuildPipeline method")
//			},
//			VerifyAssetVersionFunc: func(ctx context.Context, assetJSONPath string, version string) error {
//				panic("mock out the VerifyAssetVersion method")
//			},
//		}
//
//		// use mockedServiceBundle in code that requires ServiceBundle
//		// and then make assertions.
//
//	}
type ServiceBundleMock struct {
	// CheckLatestMARGoVersionFunc mocks the CheckLatestMARGoVersion method.
	CheckLatestMARGoVersionFunc func(ctx context.Context, versions []string) error

	// CreateAnnouncementBlogFileFunc mocks the CreateAnnouncementBlogFile method.
	CreateAnnouncementBlogFileFunc func(ctx context.Context, versions []string, user string, security bool, secret *Secret) error

	// CreateDockerImagesPRFunc mocks the CreateDockerImagesPR method.
	CreateDockerImagesPRFunc func(ctx context.Context, repo string, assetJSONPath string, manualBranch string, secret *Secret) (int, error)

	// CreateGitHubReleaseFunc mocks the CreateGitHubRelease method.
	CreateGitHubReleaseFunc func(ctx context.Context, repo string, tag string, assetJSONPath string, buildAssetDir string, secret *Secret) error

	// CreateGitHubSyncPRFunc mocks the CreateGitHubSyncPR method.
	CreateGitHubSyncPRFunc func(ctx context.Context, repo string, branch string, secret *Secret) (int, error)

	// CreateGitHubTagFunc mocks the CreateGitHubTag method.
	CreateGitHubTagFunc func(ctx context.Context, version string, repo string, tag string, commit string, secret *Secret) error

	// CreateReleaseDayTrackingIssueFunc mocks the CreateReleaseDayTrackingIssue method.
	CreateReleaseDayTrackingIssueFunc func(ctx context.Context, repo string, runner string, versions []string, secret *Secret) (int, error)

	// DownloadPipelineArtifactToDirFunc mocks the DownloadPipelineArtifactToDir method.
	DownloadPipelineArtifactToDirFunc func(ctx context.Context, buildID string, artifactName string, secret *Secret) (string, error)

	// GetTargetBranchFunc mocks the GetTargetBranch method.
	GetTargetBranchFunc func(ctx context.Context, version string) (string, error)

	// PollAzDOMirrorFunc mocks the PollAzDOMirror method.
	PollAzDOMirrorFunc func(ctx context.Context, target string, commit string, secret *Secret) error

	// PollImagesCommitFunc mocks the PollImagesCommit method.
	PollImagesCommitFunc func(ctx context.Context, versions []string, secret *Secret) (string, error)

	// PollMergedGitHubPRCommitFunc mocks the PollMergedGitHubPRCommit method.
	PollMergedGitHubPRCommitFunc func(ctx context.Context, repo string, pr int, secret *Secret) (string, error)

	// PollPipelineCompleteFunc mocks the PollPipelineComplete method.
	PollPipelineCompleteFunc func(ctx context.Context, buildID string, secret *Secret) error

	// PollUpstreamTagCommitFunc mocks the PollUpstreamTagCommit method.
	PollUpstreamTagCommitFunc func(ctx context.Context, version string) (string, error)

	// TriggerBuildPipelineFunc mocks the TriggerBuildPipeline method.
	TriggerBuildPipelineFunc func(ctx context.Context, pipelineID int, parameters map[string]string, optionalParameters map[string]string, secret *Secret) (string, error)

	// VerifyAssetVersionFunc mocks the VerifyAssetVersion method.
	VerifyAssetVersionFunc func(ctx context.Context, assetJSONPath string, version string) error

	// calls tracks calls to the methods.
	calls struct {
		// CheckLatestMARGoVersion holds details about calls to the CheckLatestMARGoVersion method.
		CheckLatestMARGoVersion []struct {
			// Ctx is the ctx argument value.
			Ctx context.Context
			// Versions is the versions argument value.
			Versions []string
		}
		// CreateAnnouncementBlogFile holds details about calls to the CreateAnnouncementBlogFile method.
		CreateAnnouncementBlogFile []struct {
			// Ctx is the ctx argument value.
			Ctx context.Context
			// Versions is the versions argument value.
			Versions []string
			// User is the user argument value.
			User string
			// Security is the security argument value.
			Security bool
			// Secret is the secret argument value.
			Secret *Secret
		}
		// CreateDockerImagesPR holds details about calls to the CreateDockerImagesPR method.
		CreateDockerImagesPR []struct {
			// Ctx is the ctx argument value.
			Ctx context.Context
			// Repo is the repo argument value.
			Repo string
			// AssetJSONPath is the assetJSONPath argument value.
			AssetJSONPath string
			// ManualBranch is the manualBranch argument value.
			ManualBranch string
			// Secret is the secret argument value.
			Secret *Secret
		}
		// CreateGitHubRelease holds details about calls to the CreateGitHubRelease method.
		CreateGitHubRelease []struct {
			// Ctx is the ctx argument value.
			Ctx context.Context
			// Repo is the repo argument value.
			Repo string
			// Tag is the tag argument value.
			Tag string
			// AssetJSONPath is the assetJSONPath argument value.
			AssetJSONPath string
			// BuildAssetDir is the buildAssetDir argument value.
			BuildAssetDir string
			// Secret is the secret argument value.
			Secret *Secret
		}
		// CreateGitHubSyncPR holds details about calls to the CreateGitHubSyncPR method.
		CreateGitHubSyncPR []struct {
			// Ctx is the ctx argument value.
			Ctx context.Context
			// Repo is the repo argument value.
			Repo string
			// Branch is the branch argument value.
			Branch string
			// Secret is the secret argument value.
			Secret *Secret
		}
		// CreateGitHubTag holds details about calls to the CreateGitHubTag method.
		CreateGitHubTag []struct {
			// Ctx is the ctx argument value.
			Ctx context.Context
			// Version is the version argument value.
			Version string
			// Repo is the repo argument value.
			Repo string
			// Tag is the tag argument value.
			Tag string
			// Commit is the commit argument value.
			Commit string
			// Secret is the secret argument value.
			Secret *Secret
		}
		// CreateReleaseDayTrackingIssue holds details about calls to the CreateReleaseDayTrackingIssue method.
		CreateReleaseDayTrackingIssue []struct {
			// Ctx is the ctx argument value.
			Ctx context.Context
			// Repo is the repo argument value.
			Repo string
			// Runner is the runner argument value.
			Runner string
			// Versions is the versions argument value.
			Versions []string
			// Secret is the secret argument value.
			Secret *Secret
		}
		// DownloadPipelineArtifactToDir holds details about calls to the DownloadPipelineArtifactToDir method.
		DownloadPipelineArtifactToDir []struct {
			// Ctx is the ctx argument value.
			Ctx context.Context
			// BuildID is the buildID argument value.
			BuildID string
			// ArtifactName is the artifactName argument value.
			ArtifactName string
			// Secret is the secret argument value.
			Secret *Secret
		}
		// GetTargetBranch holds details about calls to the GetTargetBranch method.
		GetTargetBranch []struct {
			// Ctx is the ctx argument value.
			Ctx context.Context
			// Version is the version argument value.
			Version string
		}
		// PollAzDOMirror holds details about calls to the PollAzDOMirror method.
		PollAzDOMirror []struct {
			// Ctx is the ctx argument value.
			Ctx context.Context
			// Target is the target argument value.
			Target string
			// Commit is the commit argument value.
			Commit string
			// Secret is the secret argument value.
			Secret *Secret
		}
		// PollImagesCommit holds details about calls to the PollImagesCommit method.
		PollImagesCommit []struct {
			// Ctx is the ctx argument value.
			Ctx context.Context
			// Versions is the versions argument value.
			Versions []string
			// Secret is the secret argument value.
			Secret *Secret
		}
		// PollMergedGitHubPRCommit holds details about calls to the PollMergedGitHubPRCommit method.
		PollMergedGitHubPRCommit []struct {
			// Ctx is the ctx argument value.
			Ctx context.Context
			// Repo is the repo argument value.
			Repo string
			// Pr is the pr argument value.
			Pr int
			// Secret is the secret argument value.
			Secret *Secret
		}
		// PollPipelineComplete holds details about calls to the PollPipelineComplete method.
		PollPipelineComplete []struct {
			// Ctx is the ctx argument value.
			Ctx context.Context
			// BuildID is the buildID argument value.
			BuildID string
			// Secret is the secret argument value.
			Secret *Secret
		}
		// PollUpstreamTagCommit holds details about calls to the PollUpstreamTagCommit method.
		PollUpstreamTagCommit []struct {
			// Ctx is the ctx argument value.
			Ctx context.Context
			// Version is the version argument value.
			Version string
		}
		// TriggerBuildPipeline holds details about calls to the TriggerBuildPipeline method.
		TriggerBuildPipeline []struct {
			// Ctx is the ctx argument value.
			Ctx context.Context
			// PipelineID is the pipelineID argument value.
			PipelineID int
			// Parameters is the parameters argument value.
			Parameters map[string]string
			// OptionalParameters is the optionalParameters argument value.
			OptionalParameters map[string]string
			// Secret is the secret argument value.
			Secret *Secret
		}
		// VerifyAssetVersion holds details about calls to the VerifyAssetVersion method.
		VerifyAssetVersion []struct {
			// Ctx is the ctx argument value.
			Ctx context.Context
			// AssetJSONPath is the assetJSONPath argument value.
			AssetJSONPath string
			// Version is the version argument value.
			Version string
		}
	}
	lockCheckLatestMARGoVersion       sync.RWMutex
	lockCreateAnnouncementBlogFile    sync.RWMutex
	lockCreateDockerImagesPR          sync.RWMutex
	lockCreateGitHubRelease           sync.RWMutex
	lockCreateGitHubSyncPR            sync.RWMutex
	lockCreateGitHubTag               sync.RWMutex
	lockCreateReleaseDayTrackingIssue sync.RWMutex
	lockDownloadPipelineArtifactToDir sync.RWMutex
	lockGetTargetBranch               sync.RWMutex
	lockPollAzDOMirror                sync.RWMutex
	lockPollImagesCommit              sync.RWMutex
	lockPollMergedGitHubPRCommit      sync.RWMutex
	lockPollPipelineComplete          sync.RWMutex
	lockPollUpstreamTagCommit         sync.RWMutex
	lockTriggerBuildPipeline          sync.RWMutex
	lockVerifyAssetVersion            sync.RWMutex
}

// CheckLatestMARGoVersion calls CheckLatestMARGoVersionFunc.
func (mock *ServiceBundleMock) CheckLatestMARGoVersion(ctx context.Context, versions []string) error {
	if mock.CheckLatestMARGoVersionFunc == nil {
		panic("ServiceBundleMock.CheckLatestMARGoVersionFunc: method is nil but ServiceBundle.CheckLatestMARGoVersion was just called")
	}
	callInfo := struct {
		Ctx      context.Context
		Versions []string
	}{
		Ctx:      ctx,
		Versions: versions,
	}
	mock.lockCheckLatestMARGoVersion.Lock()
	mock.calls.CheckLatestMARGoVersion = append(mock.calls.CheckLatestMARGoVersion, callInfo)
	mock.lockCheckLatestMARGoVersion.Unlock()
	return mock.CheckLatestMARGoVersionFunc(ctx, versions)
}

// CheckLatestMARGoVersionCalls gets all the calls that were made to CheckLatestMARGoVersion.
// Check the length with:
//
//	len(mockedServiceBundle.CheckLatestMARGoVersionCalls())
func (mock *ServiceBundleMock) CheckLatestMARGoVersionCalls() []struct {
	Ctx      context.Context
	Versions []string
} {
	var calls []struct {
		Ctx      context.Context
		Versions []string
	}
	mock.lockCheckLatestMARGoVersion.RLock()
	calls = mock.calls.CheckLatestMARGoVersion
	mock.lockCheckLatestMARGoVersion.RUnlock()
	return calls
}

// CreateAnnouncementBlogFile calls CreateAnnouncementBlogFileFunc.
func (mock *ServiceBundleMock) CreateAnnouncementBlogFile(ctx context.Context, versions []string, user string, security bool, secret *Secret) error {
	if mock.CreateAnnouncementBlogFileFunc == nil {
		panic("ServiceBundleMock.CreateAnnouncementBlogFileFunc: method is nil but ServiceBundle.CreateAnnouncementBlogFile was just called")
	}
	callInfo := struct {
		Ctx      context.Context
		Versions []string
		User     string
		Security bool
		Secret   *Secret
	}{
		Ctx:      ctx,
		Versions: versions,
		User:     user,
		Security: security,
		Secret:   secret,
	}
	mock.lockCreateAnnouncementBlogFile.Lock()
	mock.calls.CreateAnnouncementBlogFile = append(mock.calls.CreateAnnouncementBlogFile, callInfo)
	mock.lockCreateAnnouncementBlogFile.Unlock()
	return mock.CreateAnnouncementBlogFileFunc(ctx, versions, user, security, secret)
}

// CreateAnnouncementBlogFileCalls gets all the calls that were made to CreateAnnouncementBlogFile.
// Check the length with:
//
//	len(mockedServiceBundle.CreateAnnouncementBlogFileCalls())
func (mock *ServiceBundleMock) CreateAnnouncementBlogFileCalls() []struct {
	Ctx      context.Context
	Versions []string
	User     string
	Security bool
	Secret   *Secret
} {
	var calls []struct {
		Ctx      context.Context
		Versions []string
		User     string
		Security bool
		Secret   *Secret
	}
	mock.lockCreateAnnouncementBlogFile.RLock()
	calls = mock.calls.CreateAnnouncementBlogFile
	mock.lockCreateAnnouncementBlogFile.RUnlock()
	return calls
}

// CreateDockerImagesPR calls CreateDockerImagesPRFunc.
func (mock *ServiceBundleMock) CreateDockerImagesPR(ctx context.Context, repo string, assetJSONPath string, manualBranch string, secret *Secret) (int, error) {
	if mock.CreateDockerImagesPRFunc == nil {
		panic("ServiceBundleMock.CreateDockerImagesPRFunc: method is nil but ServiceBundle.CreateDockerImagesPR was just called")
	}
	callInfo := struct {
		Ctx           context.Context
		Repo          string
		AssetJSONPath string
		ManualBranch  string
		Secret        *Secret
	}{
		Ctx:           ctx,
		Repo:          repo,
		AssetJSONPath: assetJSONPath,
		ManualBranch:  manualBranch,
		Secret:        secret,
	}
	mock.lockCreateDockerImagesPR.Lock()
	mock.calls.CreateDockerImagesPR = append(mock.calls.CreateDockerImagesPR, callInfo)
	mock.lockCreateDockerImagesPR.Unlock()
	return mock.CreateDockerImagesPRFunc(ctx, repo, assetJSONPath, manualBranch, secret)
}

// CreateDockerImagesPRCalls gets all the calls that were made to CreateDockerImagesPR.
// Check the length with:
//
//	len(mockedServiceBundle.CreateDockerImagesPRCalls())
func (mock *ServiceBundleMock) CreateDockerImagesPRCalls() []struct {
	Ctx           context.Context
	Repo          string
	AssetJSONPath string
	ManualBranch  string
	Secret        *Secret
} {
	var calls []struct {
		Ctx           context.Context
		Repo          string
		AssetJSONPath string
		ManualBranch  string
		Secret        *Secret
	}
	mock.lockCreateDockerImagesPR.RLock()
	calls = mock.calls.CreateDockerImagesPR
	mock.lockCreateDockerImagesPR.RUnlock()
	return calls
}

// CreateGitHubRelease calls CreateGitHubReleaseFunc.
func (mock *ServiceBundleMock) CreateGitHubRelease(ctx context.Context, repo string, tag string, assetJSONPath string, buildAssetDir string, secret *Secret) error {
	if mock.CreateGitHubReleaseFunc == nil {
		panic("ServiceBundleMock.CreateGitHubReleaseFunc: method is nil but ServiceBundle.CreateGitHubRelease was just called")
	}
	callInfo := struct {
		Ctx           context.Context
		Repo          string
		Tag           string
		AssetJSONPath string
		BuildAssetDir string
		Secret        *Secret
	}{
		Ctx:           ctx,
		Repo:          repo,
		Tag:           tag,
		AssetJSONPath: assetJSONPath,
		BuildAssetDir: buildAssetDir,
		Secret:        secret,
	}
	mock.lockCreateGitHubRelease.Lock()
	mock.calls.CreateGitHubRelease = append(mock.calls.CreateGitHubRelease, callInfo)
	mock.lockCreateGitHubRelease.Unlock()
	return mock.CreateGitHubReleaseFunc(ctx, repo, tag, assetJSONPath, buildAssetDir, secret)
}

// CreateGitHubReleaseCalls gets all the calls that were made to CreateGitHubRelease.
// Check the length with:
//
//	len(mockedServiceBundle.CreateGitHubReleaseCalls())
func (mock *ServiceBundleMock) CreateGitHubReleaseCalls() []struct {
	Ctx           context.Context
	Repo          string
	Tag           string
	AssetJSONPath string
	BuildAssetDir string
	Secret        *Secret
} {
	var calls []struct {
		Ctx           context.Context
		Repo          string
		Tag           string
		AssetJSONPath string
		BuildAssetDir string
		Secret        *Secret
	}
	mock.lockCreateGitHubRelease.RLock()
	calls = mock.calls.CreateGitHubRelease
	mock.lockCreateGitHubRelease.RUnlock()
	return calls
}

// CreateGitHubSyncPR calls CreateGitHubSyncPRFunc.
func (mock *ServiceBundleMock) CreateGitHubSyncPR(ctx context.Context, repo string, branch string, secret *Secret) (int, error) {
	if mock.CreateGitHubSyncPRFunc == nil {
		panic("ServiceBundleMock.CreateGitHubSyncPRFunc: method is nil but ServiceBundle.CreateGitHubSyncPR was just called")
	}
	callInfo := struct {
		Ctx    context.Context
		Repo   string
		Branch string
		Secret *Secret
	}{
		Ctx:    ctx,
		Repo:   repo,
		Branch: branch,
		Secret: secret,
	}
	mock.lockCreateGitHubSyncPR.Lock()
	mock.calls.CreateGitHubSyncPR = append(mock.calls.CreateGitHubSyncPR, callInfo)
	mock.lockCreateGitHubSyncPR.Unlock()
	return mock.CreateGitHubSyncPRFunc(ctx, repo, branch, secret)
}

// CreateGitHubSyncPRCalls gets all the calls that were made to CreateGitHubSyncPR.
// Check the length with:
//
//	len(mockedServiceBundle.CreateGitHubSyncPRCalls())
func (mock *ServiceBundleMock) CreateGitHubSyncPRCalls() []struct {
	Ctx    context.Context
	Repo   string
	Branch string
	Secret *Secret
} {
	var calls []struct {
		Ctx    context.Context
		Repo   string
		Branch string
		Secret *Secret
	}
	mock.lockCreateGitHubSyncPR.RLock()
	calls = mock.calls.CreateGitHubSyncPR
	mock.lockCreateGitHubSyncPR.RUnlock()
	return calls
}

// CreateGitHubTag calls CreateGitHubTagFunc.
func (mock *ServiceBundleMock) CreateGitHubTag(ctx context.Context, version string, repo string, tag string, commit string, secret *Secret) error {
	if mock.CreateGitHubTagFunc == nil {
		panic("ServiceBundleMock.CreateGitHubTagFunc: method is nil but ServiceBundle.CreateGitHubTag was just called")
	}
	callInfo := struct {
		Ctx     context.Context
		Version string
		Repo    string
		Tag     string
		Commit  string
		Secret  *Secret
	}{
		Ctx:     ctx,
		Version: version,
		Repo:    repo,
		Tag:     tag,
		Commit:  commit,
		Secret:  secret,
	}
	mock.lockCreateGitHubTag.Lock()
	mock.calls.CreateGitHubTag = append(mock.calls.CreateGitHubTag, callInfo)
	mock.lockCreateGitHubTag.Unlock()
	return mock.CreateGitHubTagFunc(ctx, version, repo, tag, commit, secret)
}

// CreateGitHubTagCalls gets all the calls that were made to CreateGitHubTag.
// Check the length with:
//
//	len(mockedServiceBundle.CreateGitHubTagCalls())
func (mock *ServiceBundleMock) CreateGitHubTagCalls() []struct {
	Ctx     context.Context
	Version string
	Repo    string
	Tag     string
	Commit  string
	Secret  *Secret
} {
	var calls []struct {
		Ctx     context.Context
		Version string
		Repo    string
		Tag     string
		Commit  string
		Secret  *Secret
	}
	mock.lockCreateGitHubTag.RLock()
	calls = mock.calls.CreateGitHubTag
	mock.lockCreateGitHubTag.RUnlock()
	return calls
}

// CreateReleaseDayTrackingIssue calls CreateReleaseDayTrackingIssueFunc.
func (mock *ServiceBundleMock) CreateReleaseDayTrackingIssue(ctx context.Context, repo string, runner string, versions []string, secret *Secret) (int, error) {
	if mock.CreateReleaseDayTrackingIssueFunc == nil {
		panic("ServiceBundleMock.CreateReleaseDayTrackingIssueFunc: method is nil but ServiceBundle.CreateReleaseDayTrackingIssue was just called")
	}
	callInfo := struct {
		Ctx      context.Context
		Repo     string
		Runner   string
		Versions []string
		Secret   *Secret
	}{
		Ctx:      ctx,
		Repo:     repo,
		Runner:   runner,
		Versions: versions,
		Secret:   secret,
	}
	mock.lockCreateReleaseDayTrackingIssue.Lock()
	mock.calls.CreateReleaseDayTrackingIssue = append(mock.calls.CreateReleaseDayTrackingIssue, callInfo)
	mock.lockCreateReleaseDayTrackingIssue.Unlock()
	return mock.CreateReleaseDayTrackingIssueFunc(ctx, repo, runner, versions, secret)
}

// CreateReleaseDayTrackingIssueCalls gets all the calls that were made to CreateReleaseDayTrackingIssue.
// Check the length with:
//
//	len(mockedServiceBundle.CreateReleaseDayTrackingIssueCalls())
func (mock *ServiceBundleMock) CreateReleaseDayTrackingIssueCalls() []struct {
	Ctx      context.Context
	Repo     string
	Runner   string
	Versions []string
	Secret   *Secret
} {
	var calls []struct {
		Ctx      context.Context
		Repo     string
		Runner   string
		Versions []string
		Secret   *Secret
	}
	mock.lockCreateReleaseDayTrackingIssue.RLock()
	calls = mock.calls.CreateReleaseDayTrackingIssue
	mock.lockCreateReleaseDayTrackingIssue.RUnlock()
	return calls
}

// DownloadPipelineArtifactToDir calls DownloadPipelineArtifactToDirFunc.
func (mock *ServiceBundleMock) DownloadPipelineArtifactToDir(ctx context.Context, buildID string, artifactName string, secret *Secret) (string, error) {
	if mock.DownloadPipelineArtifactToDirFunc == nil {
		panic("ServiceBundleMock.DownloadPipelineArtifactToDirFunc: method is nil but ServiceBundle.DownloadPipelineArtifactToDir was just called")
	}
	callInfo := struct {
		Ctx          context.Context
		BuildID      string
		ArtifactName string
		Secret       *Secret
	}{
		Ctx:          ctx,
		BuildID:      buildID,
		ArtifactName: artifactName,
		Secret:       secret,
	}
	mock.lockDownloadPipelineArtifactToDir.Lock()
	mock.calls.DownloadPipelineArtifactToDir = append(mock.calls.DownloadPipelineArtifactToDir, callInfo)
	mock.lockDownloadPipelineArtifactToDir.Unlock()
	return mock.DownloadPipelineArtifactToDirFunc(ctx, buildID, artifactName, secret)
}

// DownloadPipelineArtifactToDirCalls gets all the calls that were made to DownloadPipelineArtifactToDir.
// Check the length with:
//
//	len(mockedServiceBundle.DownloadPipelineArtifactToDirCalls())
func (mock *ServiceBundleMock) DownloadPipelineArtifactToDirCalls() []struct {
	Ctx          context.Context
	BuildID      string
	ArtifactName string
	Secret       *Secret
} {
	var calls []struct {
		Ctx          context.Context
		BuildID      string
		ArtifactName string
		Secret       *Secret
	}
	mock.lockDownloadPipelineArtifactToDir.RLock()
	calls = mock.calls.DownloadPipelineArtifactToDir
	mock.lockDownloadPipelineArtifactToDir.RUnlock()
	return calls
}

// GetTargetBranch calls GetTargetBranchFunc.
func (mock *ServiceBundleMock) GetTargetBranch(ctx context.Context, version string) (string, error) {
	if mock.GetTargetBranchFunc == nil {
		panic("ServiceBundleMock.GetTargetBranchFunc: method is nil but ServiceBundle.GetTargetBranch was just called")
	}
	callInfo := struct {
		Ctx     context.Context
		Version string
	}{
		Ctx:     ctx,
		Version: version,
	}
	mock.lockGetTargetBranch.Lock()
	mock.calls.GetTargetBranch = append(mock.calls.GetTargetBranch, callInfo)
	mock.lockGetTargetBranch.Unlock()
	return mock.GetTargetBranchFunc(ctx, version)
}

// GetTargetBranchCalls gets all the calls that were made to GetTargetBranch.
// Check the length with:
//
//	len(mockedServiceBundle.GetTargetBranchCalls())
func (mock *ServiceBundleMock) GetTargetBranchCalls() []struct {
	Ctx     context.Context
	Version string
} {
	var calls []struct {
		Ctx     context.Context
		Version string
	}
	mock.lockGetTargetBranch.RLock()
	calls = mock.calls.GetTargetBranch
	mock.lockGetTargetBranch.RUnlock()
	return calls
}

// PollAzDOMirror calls PollAzDOMirrorFunc.
func (mock *ServiceBundleMock) PollAzDOMirror(ctx context.Context, target string, commit string, secret *Secret) error {
	if mock.PollAzDOMirrorFunc == nil {
		panic("ServiceBundleMock.PollAzDOMirrorFunc: method is nil but ServiceBundle.PollAzDOMirror was just called")
	}
	callInfo := struct {
		Ctx    context.Context
		Target string
		Commit string
		Secret *Secret
	}{
		Ctx:    ctx,
		Target: target,
		Commit: commit,
		Secret: secret,
	}
	mock.lockPollAzDOMirror.Lock()
	mock.calls.PollAzDOMirror = append(mock.calls.PollAzDOMirror, callInfo)
	mock.lockPollAzDOMirror.Unlock()
	return mock.PollAzDOMirrorFunc(ctx, target, commit, secret)
}

// PollAzDOMirrorCalls gets all the calls that were made to PollAzDOMirror.
// Check the length with:
//
//	len(mockedServiceBundle.PollAzDOMirrorCalls())
func (mock *ServiceBundleMock) PollAzDOMirrorCalls() []struct {
	Ctx    context.Context
	Target string
	Commit string
	Secret *Secret
} {
	var calls []struct {
		Ctx    context.Context
		Target string
		Commit string
		Secret *Secret
	}
	mock.lockPollAzDOMirror.RLock()
	calls = mock.calls.PollAzDOMirror
	mock.lockPollAzDOMirror.RUnlock()
	return calls
}

// PollImagesCommit calls PollImagesCommitFunc.
func (mock *ServiceBundleMock) PollImagesCommit(ctx context.Context, versions []string, secret *Secret) (string, error) {
	if mock.PollImagesCommitFunc == nil {
		panic("ServiceBundleMock.PollImagesCommitFunc: method is nil but ServiceBundle.PollImagesCommit was just called")
	}
	callInfo := struct {
		Ctx      context.Context
		Versions []string
		Secret   *Secret
	}{
		Ctx:      ctx,
		Versions: versions,
		Secret:   secret,
	}
	mock.lockPollImagesCommit.Lock()
	mock.calls.PollImagesCommit = append(mock.calls.PollImagesCommit, callInfo)
	mock.lockPollImagesCommit.Unlock()
	return mock.PollImagesCommitFunc(ctx, versions, secret)
}

// PollImagesCommitCalls gets all the calls that were made to PollImagesCommit.
// Check the length with:
//
//	len(mockedServiceBundle.PollImagesCommitCalls())
func (mock *ServiceBundleMock) PollImagesCommitCalls() []struct {
	Ctx      context.Context
	Versions []string
	Secret   *Secret
} {
	var calls []struct {
		Ctx      context.Context
		Versions []string
		Secret   *Secret
	}
	mock.lockPollImagesCommit.RLock()
	calls = mock.calls.PollImagesCommit
	mock.lockPollImagesCommit.RUnlock()
	return calls
}

// PollMergedGitHubPRCommit calls PollMergedGitHubPRCommitFunc.
func (mock *ServiceBundleMock) PollMergedGitHubPRCommit(ctx context.Context, repo string, pr int, secret *Secret) (string, error) {
	if mock.PollMergedGitHubPRCommitFunc == nil {
		panic("ServiceBundleMock.PollMergedGitHubPRCommitFunc: method is nil but ServiceBundle.PollMergedGitHubPRCommit was just called")
	}
	callInfo := struct {
		Ctx    context.Context
		Repo   string
		Pr     int
		Secret *Secret
	}{
		Ctx:    ctx,
		Repo:   repo,
		Pr:     pr,
		Secret: secret,
	}
	mock.lockPollMergedGitHubPRCommit.Lock()
	mock.calls.PollMergedGitHubPRCommit = append(mock.calls.PollMergedGitHubPRCommit, callInfo)
	mock.lockPollMergedGitHubPRCommit.Unlock()
	return mock.PollMergedGitHubPRCommitFunc(ctx, repo, pr, secret)
}

// PollMergedGitHubPRCommitCalls gets all the calls that were made to PollMergedGitHubPRCommit.
// Check the length with:
//
//	len(mockedServiceBundle.PollMergedGitHubPRCommitCalls())
func (mock *ServiceBundleMock) PollMergedGitHubPRCommitCalls() []struct {
	Ctx    context.Context
	Repo   string
	Pr     int
	Secret *Secret
} {
	var calls []struct {
		Ctx    context.Context
		Repo   string
		Pr     int
		Secret *Secret
	}
	mock.lockPollMergedGitHubPRCommit.RLock()
	calls = mock.calls.PollMergedGitHubPRCommit
	mock.lockPollMergedGitHubPRCommit.RUnlock()
	return calls
}

// PollPipelineComplete calls PollPipelineCompleteFunc.
func (mock *ServiceBundleMock) PollPipelineComplete(ctx context.Context, buildID string, secret *Secret) error {
	if mock.PollPipelineCompleteFunc == nil {
		panic("ServiceBundleMock.PollPipelineCompleteFunc: method is nil but ServiceBundle.PollPipelineComplete was just called")
	}
	callInfo := struct {
		Ctx     context.Context
		BuildID string
		Secret  *Secret
	}{
		Ctx:     ctx,
		BuildID: buildID,
		Secret:  secret,
	}
	mock.lockPollPipelineComplete.Lock()
	mock.calls.PollPipelineComplete = append(mock.calls.PollPipelineComplete, callInfo)
	mock.lockPollPipelineComplete.Unlock()
	return mock.PollPipelineCompleteFunc(ctx, buildID, secret)
}

// PollPipelineCompleteCalls gets all the calls that were made to PollPipelineComplete.
// Check the length with:
//
//	len(mockedServiceBundle.PollPipelineCompleteCalls())
func (mock *ServiceBundleMock) PollPipelineCompleteCalls() []struct {
	Ctx     context.Context
	BuildID string
	Secret  *Secret
} {
	var calls []struct {
		Ctx     context.Context
		BuildID string
		Secret  *Secret
	}
	mock.lockPollPipelineComplete.RLock()
	calls = mock.calls.PollPipelineComplete
	mock.lockPollPipelineComplete.RUnlock()
	return calls
}

// PollUpstreamTagCommit calls PollUpstreamTagCommitFunc.
func (mock *ServiceBundleMock) PollUpstreamTagCommit(ctx context.Context, version string) (string, error) {
	if mock.PollUpstreamTagCommitFunc == nil {
		panic("ServiceBundleMock.PollUpstreamTagCommitFunc: method is nil but ServiceBundle.PollUpstreamTagCommit was just called")
	}
	callInfo := struct {
		Ctx     context.Context
		Version string
	}{
		Ctx:     ctx,
		Version: version,
	}
	mock.lockPollUpstreamTagCommit.Lock()
	mock.calls.PollUpstreamTagCommit = append(mock.calls.PollUpstreamTagCommit, callInfo)
	mock.lockPollUpstreamTagCommit.Unlock()
	return mock.PollUpstreamTagCommitFunc(ctx, version)
}

// PollUpstreamTagCommitCalls gets all the calls that were made to PollUpstreamTagCommit.
// Check the length with:
//
//	len(mockedServiceBundle.PollUpstreamTagCommitCalls())
func (mock *ServiceBundleMock) PollUpstreamTagCommitCalls() []struct {
	Ctx     context.Context
	Version string
} {
	var calls []struct {
		Ctx     context.Context
		Version string
	}
	mock.lockPollUpstreamTagCommit.RLock()
	calls = mock.calls.PollUpstreamTagCommit
	mock.lockPollUpstreamTagCommit.RUnlock()
	return calls
}

// TriggerBuildPipeline calls TriggerBuildPipelineFunc.
func (mock *ServiceBundleMock) TriggerBuildPipeline(ctx context.Context, pipelineID int, parameters map[string]string, optionalParameters map[string]string, secret *Secret) (string, error) {
	if mock.TriggerBuildPipelineFunc == nil {
		panic("ServiceBundleMock.TriggerBuildPipelineFunc: method is nil but ServiceBundle.TriggerBuildPipeline was just called")
	}
	callInfo := struct {
		Ctx                context.Context
		PipelineID         int
		Parameters         map[string]string
		OptionalParameters map[string]string
		Secret             *Secret
	}{
		Ctx:                ctx,
		PipelineID:         pipelineID,
		Parameters:         parameters,
		OptionalParameters: optionalParameters,
		Secret:             secret,
	}
	mock.lockTriggerBuildPipeline.Lock()
	mock.calls.TriggerBuildPipeline = append(mock.calls.TriggerBuildPipeline, callInfo)
	mock.lockTriggerBuildPipeline.Unlock()
	return mock.TriggerBuildPipelineFunc(ctx, pipelineID, parameters, optionalParameters, secret)
}

// TriggerBuildPipelineCalls gets all the calls that were made to TriggerBuildPipeline.
// Check the length with:
//
//	len(mockedServiceBundle.TriggerBuildPipelineCalls())
func (mock *ServiceBundleMock) TriggerBuildPipelineCalls() []struct {
	Ctx                context.Context
	PipelineID         int
	Parameters         map[string]string
	OptionalParameters map[string]string
	Secret             *Secret
} {
	var calls []struct {
		Ctx                context.Context
		PipelineID         int
		Parameters         map[string]string
		OptionalParameters map[string]string
		Secret             *Secret
	}
	mock.lockTriggerBuildPipeline.RLock()
	calls = mock.calls.TriggerBuildPipeline
	mock.lockTriggerBuildPipeline.RUnlock()
	return calls
}

// VerifyAssetVersion calls VerifyAssetVersionFunc.
func (mock *ServiceBundleMock) VerifyAssetVersion(ctx context.Context, assetJSONPath string, version string) error {
	if mock.VerifyAssetVersionFunc == nil {
		panic("ServiceBundleMock.VerifyAssetVersionFunc: method is nil but ServiceBundle.VerifyAssetVersion was just called")
	}
	callInfo := struct {
		Ctx           context.Context
		AssetJSONPath string
		Version       string
	}{
		Ctx:           ctx,
		AssetJSONPath: assetJSONPath,
		Version:       version,
	}
	mock.lockVerifyAssetVersion.Lock()
	mock.calls.VerifyAssetVersion = append(mock.calls.VerifyAssetVersion, callInfo)
	mock.lockVerifyAssetVersion.Unlock()
	return mock.VerifyAssetVersionFunc(ctx, assetJSONPath, version)
}

// VerifyAssetVersionCalls gets all the calls that were made to VerifyAssetVersion.
// Check the length with:
//
//	len(mockedServiceBundle.VerifyAssetVersionCalls())
func (mock *ServiceBundleMock) VerifyAssetVersionCalls() []struct {
	Ctx           context.Context
	AssetJSONPath string
	Version       string
} {
	var calls []struct {
		Ctx           context.Context
		AssetJSONPath string
		Version       string
	}
	mock.lockVerifyAssetVersion.RLock()
	calls = mock.calls.VerifyAssetVersion
	mock.lockVerifyAssetVersion.RUnlock()
	return calls
}