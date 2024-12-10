// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package releasesteps

import (
	"context"
	"encoding/json"
	"fmt"
	"hash/crc32"
	"path/filepath"
	"time"

	"github.com/microsoft/go-infra/cmd/releaseagent/internal/coordinator"
)

//go:generate moq -out ServiceBundle_moq_test.go . ServiceBundle

// Input is the collection of inputs for a given release that don't change. They are provided once
// by the release runner and stay the same upon retry.
type Input struct {
	// Versions is a list of versions to release.
	Versions []string

	// Security is true if any of the versions contains security fixes.
	Security bool

	// RunnerGitHubUser is the GitHub username of the dev in charge of this release. They are
	// @-tagged in the release issue if their input is required. This username is also be mapped to
	// a WordPress user for the release blog post.
	//
	// "ghost" is a special value that indicates nobody should be notified. It is a username that
	// GitHub has reserved as a placeholder.
	RunnerGitHubUser string

	// ReleaseConfigVariableGroup is the name of the AzDO variable group containing the release
	// configuration, mainly secrets. This is passed to child pipelines that need access.
	ReleaseConfigVariableGroup string

	// TargetRepo is microsoft/go, or a custom override for testing.
	TargetRepo     string
	TargetAzDORepo string

	TargetGoImagesRepo     string
	TargetAzDOGoImagesRepo string

	MicrosoftGoPipeline          int
	MicrosoftGoInnerloopPipeline int
	MicrosoftGoImagesPipeline    int
	MicrosoftGoAkaMSPipeline     int
	AzureLinuxCreatePRPipeline   int
}

func (i *Input) checksum() (uint32, error) {
	marshal, err := json.Marshal(i)
	if err != nil {
		return 0, err
	}
	return crc32.ChecksumIEEE(marshal), nil
}

// Secret is a collection of secrets necessary to perform the top-level actions in a release. These
// are intentionally not part of Input, as they may change if e.g. a secret is cycled while a
// release is paused and then needs to be resumed. (The Input checksum would make this difficult.)
type Secret struct {
	GitHubPAT         string
	GitHubReviewerPAT string
	AzDOPAT           string
}

// State is the state of a release, saved and restored between retries.
// In theory, the release runner might modify this if things go wrong.
type State struct {
	// InputChecksum of the Input that started this release. This is used to ensure the
	// input hasn't unintentionally changed between retries. It isn't a security feature and isn't
	// stored beyond a single release process.
	//
	// The most likely mistake this is likely to detect is that the release runner, while trying to
	// start a retry, copies the state correctly, but presses the wrong "Run" button, causing the
	// wrong input to be filled in by AzDO.
	InputChecksum uint32

	// Day is the release day's state.
	Day *DayState

	// Versions maps each entry from the Input.Versions slice to its state.
	Versions map[string]*VersionState
}

// DayState is the state of the "release day" not associated with a specific version.
type DayState struct {
	// ReleaseIssue is the ID of the release issue to supply with updates.
	ReleaseIssue int

	GoImagesCommit          string
	GoImagesOfficialBuildID string

	AnnouncementWritten bool
	MARVersionChecked   bool
}

// VersionState is the state of a single version's release.
type VersionState struct {
	UpdatePR         int
	Commit           string
	OfficialBuildID  string
	InnerloopBuildID string

	ImageUpdatePR int
	ImagesUpdated bool

	GitHubTag     string
	GitHubRelease string

	AkaMSBuildID string
	AkaMSUpdated bool

	AzureLinuxUpdateBuildID string
	AzureLinuxPRSubmitted   bool
}

// ServiceBundle is all the ways the release steps can interact with the outside world. This can be
// mocked for testing.
//
// If a method returns an error, other returned values must be zero. Retry logic depends on this.
type ServiceBundle interface {
	CreateReleaseDayTrackingIssue(ctx context.Context, repo, runner string, versions []string, secret *Secret) (int, error)
	PollUpstreamTagCommit(ctx context.Context, version string) (string, error)
	CreateGitHubSyncPR(ctx context.Context, repo, branch string, secret *Secret) (int, error)
	PollMergedGitHubPRCommit(ctx context.Context, repo string, pr int, secret *Secret) (string, error)
	PollAzDOMirror(ctx context.Context, target, commit string, secret *Secret) error
	GetTargetBranch(ctx context.Context, version string) (string, error)
	TriggerBuildPipeline(ctx context.Context, pipelineID int, parameters, optionalParameters map[string]string, secret *Secret) (string, error)
	PollPipelineComplete(ctx context.Context, buildID string, secret *Secret) error
	DownloadPipelineArtifactToDir(ctx context.Context, buildID, artifactName string, secret *Secret) (string, error)
	VerifyAssetVersion(ctx context.Context, assetJSONPath string, version string) error
	CreateGitHubTag(ctx context.Context, version, repo, tag, commit string, secret *Secret) error
	CreateGitHubRelease(ctx context.Context, repo, tag, assetJSONPath, buildAssetDir string, secret *Secret) error
	CreateDockerImagesPR(ctx context.Context, repo, assetJSONPath, manualBranch string, secret *Secret) (int, error)
	PollImagesCommit(ctx context.Context, versions []string, secret *Secret) (string, error)
	CheckLatestMARGoVersion(ctx context.Context, versions []string) error
	CreateAnnouncementBlogFile(ctx context.Context, versions []string, user string, security bool, secret *Secret) error
}

// CreateStepGraph creates the steps for a release of one or more versions of Microsoft Go. The
// returned step graph is not running.
//
// If rs is nil, creates a new empty state that indicates no release work has been done yet.
// Otherwise, rs is used to resume an existing release. Returns rs or the new State so it can be
// used to resume a future release.
//
// While any step is running, it may modify State, so it is not safe to access the returned State.
// When all steps are complete (success or fail), State can then be safely used.
//
// Implementation note: this function should only contain coordination code (moving inputs/outputs
// between steps through the State and synchronizing). All work involving external resources should
// be done by calling methods on the ServiceBundle.
func CreateStepGraph(ri *Input, secret *Secret, rs *State, sb ServiceBundle) ([]*coordinator.Step, *State, error) {
	if ri == nil || len(ri.Versions) == 0 {
		return nil, nil, fmt.Errorf("no versions to release")
	}

	// Don't use simple "err" variable name here to avoid having "err" in scope during step
	// creation. It is easy to accidentally capture it while writing new steps, and that results in
	// a data race.
	riChecksum, checksumErr := ri.checksum()
	if checksumErr != nil {
		return nil, nil, fmt.Errorf("failed to checksum release input: %v", checksumErr)
	}

	// Either create a new state or validate the existing one's checksum.
	if rs == nil {
		rs = &State{
			InputChecksum: riChecksum,
		}
	} else if riChecksum != rs.InputChecksum {
		return nil, nil, fmt.Errorf(
			"release input doesn't match initial input: expected checksum %v (from state), got %v (by calculation)",
			rs.InputChecksum, riChecksum)
	}

	// Ensure state is initialized.
	if rs.Versions == nil {
		rs.Versions = make(map[string]*VersionState)
	}
	for _, version := range ri.Versions {
		if _, ok := rs.Versions[version]; !ok {
			rs.Versions[version] = &VersionState{}
		}
	}
	if rs.Day == nil {
		rs.Day = &DayState{}
	}

	createStatusReportIssue := coordinator.NewRootStep(
		"Create release day issue", coordinator.NoTimeout,
		func(ctx context.Context) error {
			if rs.Day.ReleaseIssue != 0 {
				return nil
			}
			var err error
			rs.Day.ReleaseIssue, err = sb.CreateReleaseDayTrackingIssue(
				ctx, ri.TargetRepo, ri.RunnerGitHubUser, ri.Versions, secret)
			return err
		},
	)

	var versionCompleteSteps []*coordinator.Step
	var versionSpecificPublishSteps []*coordinator.Step

	for _, version := range ri.Versions {
		vs := rs.Versions[version]
		name := func(n string) string {
			return fmt.Sprintf("%s, %s", n, version)
		}

		syncUpdate := coordinator.NewStep(
			name("Sync"),
			6*time.Hour,
			func(ctx context.Context) error {
				if vs.UpdatePR != 0 {
					return nil
				}
				upstreamCommit, err := sb.PollUpstreamTagCommit(ctx, version)
				if err != nil {
					return err
				}
				vs.UpdatePR, err = sb.CreateGitHubSyncPR(ctx, ri.TargetRepo, upstreamCommit, secret)
				return err
			},
			createStatusReportIssue,
		).Then(
			name("⌚ Wait for PR merge"),
			90*time.Minute,
			func(ctx context.Context) error {
				if vs.Commit != "" {
					return nil
				}
				var err error
				vs.Commit, err = sb.PollMergedGitHubPRCommit(ctx, ri.TargetRepo, vs.UpdatePR, secret)
				return err
			},
		).Then(
			name("⌚ Wait for AzDO sync"),
			// Just over 15 minute timeout for mirroring. See https://github.com/microsoft/go-lab/issues/124
			16*time.Minute,
			func(ctx context.Context) error {
				return sb.PollAzDOMirror(ctx, ri.TargetAzDORepo, vs.Commit, secret)
			},
		)

		officialBuild := coordinator.NewStep(
			name("🚀 Trigger official build"),
			5*time.Minute,
			func(ctx context.Context) error {
				if vs.OfficialBuildID != "" {
					return nil
				}
				var err error
				vs.OfficialBuildID, err = sb.TriggerBuildPipeline(ctx, ri.MicrosoftGoPipeline, nil, nil, secret)
				return err
			},
			syncUpdate,
		).Then(
			name("⌚ Wait for official build"),
			3*time.Hour,
			func(ctx context.Context) error {
				return sb.PollPipelineComplete(ctx, vs.OfficialBuildID, secret)
			},
		)

		testOfficialBuildCommit := coordinator.NewStep(
			name("🚀 Trigger innerloop build"),
			5*time.Minute,
			func(ctx context.Context) error {
				if vs.InnerloopBuildID != "" {
					return nil
				}
				var err error
				vs.InnerloopBuildID, err = sb.TriggerBuildPipeline(ctx, ri.MicrosoftGoInnerloopPipeline, nil, nil, secret)
				return err
			},
			syncUpdate,
		).Then(
			name("⌚ Wait for innerloop build"),
			3*time.Hour,
			func(ctx context.Context) error {
				return sb.PollPipelineComplete(ctx, vs.InnerloopBuildID, secret)
			},
		)

		readyForPublish := coordinator.NewIndicatorStep(
			name("✅ Artifacts ok to publish"),
			officialBuild,
			testOfficialBuildCommit,
		)

		// Download is unique to the build machine, so it isn't stored in "vs" persistent state.
		// The downloads are always performed even if all the steps that would depend on them are
		// being skipped--for example, if we resume an existing, nearly complete release.
		//
		// Skipping the downloads could be done, but it's simpler to always download them and the
		// time savings are not yet clear.
		var (
			assetJSONPath string
			artifactsDir  string
		)

		downloadAssetMetadata := coordinator.NewStep(
			name("Download asset metadata"),
			15*time.Minute,
			func(ctx context.Context) error {
				dir, err := sb.DownloadPipelineArtifactToDir(
					ctx,
					vs.OfficialBuildID,
					"BuildAssets",
					secret,
				)
				if err != nil {
					return err
				}
				assetJSONPath = filepath.Join(dir, "assets.json")
				return sb.VerifyAssetVersion(ctx, assetJSONPath, version)
			},
			officialBuild,
		)

		downloadArtifacts := coordinator.NewStep(
			name("Download artifacts"),
			15*time.Minute,
			func(ctx context.Context) error {
				var err error
				artifactsDir, err = sb.DownloadPipelineArtifactToDir(
					ctx,
					vs.OfficialBuildID,
					"Binaries Signed",
					secret,
				)
				return err
			},
			officialBuild,
		)

		githubPublish := coordinator.NewStep(
			name("🎓 Create GitHub tag"),
			5*time.Minute,
			func(ctx context.Context) error {
				if vs.GitHubTag != "" {
					return nil
				}
				tag := fmt.Sprintf("v%s", version)
				err := sb.CreateGitHubTag(ctx, version, ri.TargetRepo, tag, vs.Commit, secret)
				if err != nil {
					return err
				}
				vs.GitHubTag = tag
				return nil
			},
			readyForPublish,
		).Then(
			name("🎓 Create GitHub release"),
			15*time.Minute,
			func(ctx context.Context) error {
				if vs.GitHubRelease != "" {
					return nil
				}
				err := sb.CreateGitHubRelease(ctx, ri.TargetRepo, vs.GitHubTag, assetJSONPath, artifactsDir, secret)
				if err != nil {
					return err
				}
				vs.GitHubRelease = vs.GitHubTag
				return nil
			},
			downloadAssetMetadata, downloadArtifacts,
		)

		akaMSPublish := coordinator.NewStep(
			name("🎓 Update aka.ms links"),
			30*time.Minute,
			func(ctx context.Context) error {
				if vs.AkaMSBuildID == "" {
					var err error
					vs.AkaMSBuildID, err = sb.TriggerBuildPipeline(ctx, ri.MicrosoftGoAkaMSPipeline, nil, nil, secret)
					if err != nil {
						return err
					}
				}
				if !vs.AkaMSUpdated {
					if err := sb.PollPipelineComplete(ctx, vs.AkaMSBuildID, secret); err != nil {
						return err
					}
					vs.AkaMSUpdated = true
				}
				return nil
			},
			readyForPublish, downloadAssetMetadata,
		)

		dockerfilePublish := coordinator.NewStep(
			name("Update Dockerfiles"),
			120*time.Minute,
			func(ctx context.Context) error {
				if vs.ImageUpdatePR == 0 {
					var err error
					vs.ImageUpdatePR, err = sb.CreateDockerImagesPR(ctx, ri.TargetRepo, assetJSONPath, "", secret)
					if err != nil {
						return err
					}
				}
				if !vs.ImagesUpdated {
					var err error
					_, err = sb.PollMergedGitHubPRCommit(ctx, ri.TargetRepo, vs.ImageUpdatePR, secret)
					if err != nil {
						return err
					}
					vs.ImagesUpdated = true
				}
				return nil
			},
			readyForPublish, downloadAssetMetadata,
		)
		versionCompleteSteps = append(versionCompleteSteps, coordinator.NewIndicatorStep(
			name("✅ microsoft/go publish and go-images PR complete"),
			githubPublish,
			akaMSPublish,
			dockerfilePublish,
		))

		azureLinuxPRPublish := coordinator.NewStep(
			name("🚀 Trigger Azure Linux PR creation"),
			15*time.Minute,
			func(ctx context.Context) error {
				if vs.AzureLinuxUpdateBuildID == "" {
					var err error
					vs.AzureLinuxUpdateBuildID, err = sb.TriggerBuildPipeline(ctx, ri.AzureLinuxCreatePRPipeline, nil, nil, secret)
					if err != nil {
						return err
					}
				}
				if !vs.AzureLinuxPRSubmitted {
					if err := sb.PollPipelineComplete(ctx, vs.AzureLinuxUpdateBuildID, secret); err != nil {
						return err
					}
					vs.AzureLinuxPRSubmitted = true
				}
				// Note: we don't keep track of the PR inside this process because it may take
				// an arbitrary time to get approval to merge.
				return nil
			},
			readyForPublish,
		)

		versionSpecificPublishSteps = append(versionSpecificPublishSteps, coordinator.NewIndicatorStep(
			name("✅ External publish complete"),
			azureLinuxPRPublish,
		))
	}

	versionsComplete := coordinator.NewIndicatorStep(
		"✅ All microsoft/go publish and go-images PRs complete",
		versionCompleteSteps...,
	)

	imagesReady := coordinator.NewStep(
		"Get go-images commit",
		15*time.Minute,
		func(ctx context.Context) error {
			if rs.Day.GoImagesCommit == "" {
				var err error
				rs.Day.GoImagesCommit, err = sb.PollImagesCommit(ctx, ri.Versions, secret)
				if err != nil {
					return err
				}
			}
			return sb.PollAzDOMirror(ctx, ri.TargetAzDOGoImagesRepo, rs.Day.GoImagesCommit, secret)
		},
		versionsComplete,
	).Then(
		"🚀 Trigger go-image build/publish",
		5*time.Minute,
		func(ctx context.Context) error {
			if rs.Day.GoImagesOfficialBuildID != "" {
				return nil
			}
			var err error
			rs.Day.GoImagesOfficialBuildID, err = sb.TriggerBuildPipeline(ctx, ri.MicrosoftGoImagesPipeline, nil, nil, secret)
			return err
		},
	).Then(
		"⌚ Wait for go-image build/publish",
		2*time.Hour,
		func(ctx context.Context) error {
			return sb.PollPipelineComplete(ctx, rs.Day.GoImagesOfficialBuildID, secret)
		},
	).Then(
		"🌊 Check published image version",
		15*time.Minute,
		func(ctx context.Context) error {
			if rs.Day.MARVersionChecked {
				return nil
			}
			if err := sb.CheckLatestMARGoVersion(ctx, ri.Versions); err != nil {
				return err
			}
			rs.Day.MARVersionChecked = true
			return nil
		},
	)

	createBlog := coordinator.NewStep(
		"📰 Create blog post markdown",
		5*time.Minute,
		func(ctx context.Context) error {
			if rs.Day.AnnouncementWritten {
				return nil
			}
			if err := sb.CreateAnnouncementBlogFile(ctx, ri.Versions, ri.RunnerGitHubUser, ri.Security, secret); err != nil {
				return err
			}
			rs.Day.AnnouncementWritten = true
			return nil
		},
		versionsComplete, imagesReady,
	)

	completeStep := coordinator.NewIndicatorStep(
		"✅ Complete",
		append(
			versionSpecificPublishSteps,
			imagesReady,
			createBlog,
		)...,
	)

	allSteps, err := completeStep.TransitiveDependencies()
	if err != nil {
		return nil, nil, err
	}
	return allSteps, rs, nil
}
