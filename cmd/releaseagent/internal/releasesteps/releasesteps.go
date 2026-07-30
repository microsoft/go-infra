// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package releasesteps

import (
	"context"
	"encoding/json"
	"fmt"
	"hash/crc32"
	"path/filepath"
	"sync"
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

	// ReleaseIssue is an existing microsoft/go tracking issue to update, or zero for none. The
	// focused go-images flow does not create this issue itself.
	ReleaseIssue int

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
	Day DayState

	// Versions maps each entry from the Input.Versions slice to its state.
	Versions map[string]*VersionState
}

// DayState is the state of the "release day" not associated with a specific version.
type DayState struct {
	// ReleaseIssue is the ID of the release issue to supply with updates.
	ReleaseIssue int

	GoImagesCommit                    string
	GoImagesSourceBranch              string
	GoImagesOfficialBuildID           string
	GoImagesReleaseBuildID            string
	GoImagesReleaseResult             string
	GoImagesReleaseComplete           bool
	GoImagesReleaseImported           bool
	GoImagesReleaseParameters         map[string]string
	GoImagesDemoBuildID               string
	GoImagesDemoSourceValidated       bool
	GoImagesDemoSourceValidationError string
	GoImagesDemoQueueAttempted        bool
	GoImagesDemoComplete              bool
	GoImagesDemoParameters            map[string]string

	AnnouncementWritten bool
	MARVersionChecked   bool
}

// VersionState is the state of a single version's release.
type VersionState struct {
	UpstreamCommit   string
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

// GoImagesReleaseService is the intentionally narrow external surface required by the first UI
// integration. Implementing it cannot accidentally enable unrelated GitHub or publishing steps.
type GoImagesReleaseService interface {
	TriggerBuildPipeline(ctx context.Context, pipelineID int, parameters, optionalParameters map[string]string, secret *Secret) (string, error)
	PollPipelineComplete(ctx context.Context, buildID string, secret *Secret) error
}

// StateCheckpoint durably records state. The state pointer is valid only for the duration of the
// call and must not be retained or modified. It is called while release-state mutations are
// serialized, so the implementation may safely encode the complete state.
type StateCheckpoint func(ctx context.Context, state *State) error

type stateAccess struct {
	mu         sync.Mutex
	state      *State
	checkpoint StateCheckpoint
	dirty      bool
}

func (a *stateAccess) update(ctx context.Context, update func(*State)) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	update(a.state)
	if a.checkpoint == nil {
		return nil
	}
	a.dirty = true
	return a.flushLocked(ctx)
}

func (a *stateAccess) flush(ctx context.Context) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.flushLocked(ctx)
}

func (a *stateAccess) flushLocked(ctx context.Context) error {
	if !a.dirty || a.checkpoint == nil {
		return nil
	}
	if err := a.checkpoint(ctx, a.state); err != nil {
		return err
	}
	a.dirty = false
	return nil
}

func stateValue[T any](a *stateAccess, value func(*State) T) T {
	a.mu.Lock()
	defer a.mu.Unlock()
	return value(a.state)
}

// Common timeout values. The goal is for each timeout to be low enough to improve response time
// when manual intervention is necessary, but high enough that they don't trip on transient issues.
const (
	// noTimeout is for steps where there's no cause for concern if it takes forever. Waiting for
	// an external manual process is the only current use case.
	//
	// A step that "always" completes very quickly shouldn't use this timeout: if a bug or service
	// issue causes the step to take a long time, it's important to time out to alert the release
	// runner that something has gone wrong.
	noTimeout = coordinator.NoTimeout

	// shortTimeout is for steps that should complete quickly, like API calls. Even if the API call
	// involves a significant upload or download, this timeout may be enough: the build machines
	// tend to have fast network connections.
	shortTimeout = 10 * time.Minute

	// internalMirrorTimeout for mirroring a commit from GitHub to AzDO. Just over 15 minutes.
	// See https://github.com/microsoft/go-lab/issues/124
	internalMirrorTimeout = 16 * time.Minute

	// Timeouts for specific pipelines that we trigger and wait for during the release process.
	// Some might be the same, but they are independent and roughly tuned to the specific pipeline.

	microsoftGoPRCITimeout        = 1*time.Hour + 30*time.Minute
	microsoftGoOfficialCITimeout  = 3 * time.Hour
	microsoftGoInnerloopCITimeout = 2 * time.Hour

	microsoftGoImagesPRCITimeout       = 2 * time.Hour
	microsoftGoImagesOfficialCITimeout = 2 * time.Hour
)

// GoImagesPipelineParameters returns the runtime parameters accepted by the direct
// microsoft-go-images pipeline. The _info parameter is fixed and informational. The operational
// production defaults build from the new run's own artifacts and publish to public/. The release
// UI does not currently queue this pipeline.
func GoImagesPipelineParameters() map[string]string {
	return map[string]string{
		"_info":                    "🔵  go-docker-rolling-internal-pipeline.yml  🔵 🔵",
		"sourceBuildPipelineRunId": "$(Build.BuildId)",
		"publishRepoPrefix":        "public/",
	}
}

// GoImagesProductionDemoPipelineParameters returns the only parameter set the release UI may send
// to the authorized real demo pipeline. It builds, signs, and publishes production images under
// public/ using the official pipeline definition.
func GoImagesProductionDemoPipelineParameters() map[string]string {
	return map[string]string{
		"_info":                    "🔵  go-docker-rolling-internal-pipeline.yml  🔵 🔵",
		"sourceBuildPipelineRunId": "$(Build.BuildId)",
		"publishRepoPrefix":        "public/",
	}
}

// CreateGoImagesPipelineGraph creates the initial focused workflow that queues and monitors
// the direct microsoft-go-images pipeline as one coarse-grained operation.
func CreateGoImagesPipelineGraph(
	ri *Input,
	secret *Secret,
	rs *State,
	sb GoImagesReleaseService,
) ([]*coordinator.Step, *State, error) {
	return CreateGoImagesPipelineGraphWithCheckpoint(ri, secret, rs, sb, nil)
}

// CreateGoImagesPipelineGraphWithCheckpoint is like CreateGoImagesPipelineGraph and
// durably records the queued pipeline ID and successful completion.
func CreateGoImagesPipelineGraphWithCheckpoint(
	ri *Input,
	secret *Secret,
	rs *State,
	sb GoImagesReleaseService,
	checkpoint StateCheckpoint,
) ([]*coordinator.Step, *State, error) {
	if ri == nil || ri.MicrosoftGoImagesPipeline == 0 {
		return nil, nil, fmt.Errorf("no go-images pipeline specified")
	}
	var err error
	rs, err = initializeState(ri, rs)
	if err != nil {
		return nil, nil, err
	}
	if rs.Day.ReleaseIssue == 0 {
		rs.Day.ReleaseIssue = ri.ReleaseIssue
	}
	state := &stateAccess{state: rs, checkpoint: checkpoint}
	parameters := GoImagesPipelineParameters()

	queue := coordinator.NewRootStep(
		"go-images.pipeline.queue",
		"🚀 Queue go-images pipeline",
		shortTimeout,
		func(ctx context.Context) error {
			if stateValue(state, func(s *State) string { return s.Day.GoImagesReleaseBuildID }) != "" {
				return nil
			}
			buildID, err := sb.TriggerBuildPipeline(
				ctx,
				ri.MicrosoftGoImagesPipeline,
				parameters,
				nil,
				secret,
			)
			if err != nil {
				return err
			}
			return state.update(ctx, func(s *State) {
				s.Day.GoImagesReleaseBuildID = buildID
				s.Day.GoImagesReleaseImported = false
				s.Day.GoImagesReleaseParameters = cloneStringMap(parameters)
			})
		},
	)
	wait := queue.Then(
		"go-images.pipeline.wait",
		"⌚ Wait for go-images pipeline",
		microsoftGoImagesOfficialCITimeout,
		func(ctx context.Context) error {
			if stateValue(state, func(s *State) bool { return s.Day.GoImagesReleaseComplete }) {
				return nil
			}
			buildID := stateValue(state, func(s *State) string { return s.Day.GoImagesReleaseBuildID })
			if err := sb.PollPipelineComplete(ctx, buildID, secret); err != nil {
				return err
			}
			return state.update(ctx, func(s *State) {
				s.Day.GoImagesReleaseComplete = true
			})
		},
	)
	complete := coordinator.NewIndicatorStep(
		"go-images.pipeline.complete",
		"✅ Go-images pipeline complete",
		wait,
	)
	steps, err := complete.TransitiveDependencies()
	if err != nil {
		return nil, nil, err
	}
	wrapStepsWithStateFlush(steps, state, checkpoint)
	return steps, rs, nil
}

// CreateGoImagesProductionDemoGraphWithCheckpoint queues and monitors an allowlisted official
// go-images build. The selected completed official run supplies the exact source commit. The
// service implementation must independently enforce the pipeline, branch, commit, and parameters.
func CreateGoImagesProductionDemoGraphWithCheckpoint(
	ri *Input,
	secret *Secret,
	rs *State,
	pipelineID int,
	sb GoImagesReleaseService,
	checkpoint StateCheckpoint,
) ([]*coordinator.Step, *State, error) {
	if ri == nil || pipelineID <= 0 {
		return nil, nil, fmt.Errorf("no production go-images demo pipeline specified")
	}
	var err error
	rs, err = initializeState(ri, rs)
	if err != nil {
		return nil, nil, err
	}
	if !rs.Day.GoImagesReleaseImported || !rs.Day.GoImagesReleaseComplete || rs.Day.GoImagesReleaseResult != "succeeded" ||
		!rs.Day.GoImagesDemoSourceValidated ||
		rs.Day.GoImagesReleaseBuildID == "" || rs.Day.GoImagesCommit == "" ||
		rs.Day.GoImagesSourceBranch != "refs/heads/microsoft/main" {

		return nil, nil, fmt.Errorf("a pipeline 1023 run with result succeeded from microsoft/main must be imported first")
	}
	state := &stateAccess{state: rs, checkpoint: checkpoint}
	parameters := GoImagesProductionDemoPipelineParameters()

	queue := coordinator.NewRootStep(
		"go-images.production-demo.queue",
		"🚀 Queue production go-images demo",
		shortTimeout,
		func(ctx context.Context) error {
			if stateValue(state, func(s *State) string { return s.Day.GoImagesDemoBuildID }) != "" {
				return nil
			}
			if !stateValue(state, func(s *State) bool { return s.Day.GoImagesDemoQueueAttempted }) {
				if err := state.update(ctx, func(s *State) {
					s.Day.GoImagesDemoQueueAttempted = true
				}); err != nil {
					return err
				}
			}
			buildID, err := sb.TriggerBuildPipeline(ctx, pipelineID, parameters, nil, secret)
			if err != nil {
				return err
			}
			return state.update(ctx, func(s *State) {
				s.Day.GoImagesDemoBuildID = buildID
				s.Day.GoImagesDemoParameters = cloneStringMap(parameters)
			})
		},
	)
	wait := queue.Then(
		"go-images.production-demo.wait",
		"⌚ Wait for production go-images demo",
		microsoftGoImagesOfficialCITimeout,
		func(ctx context.Context) error {
			if stateValue(state, func(s *State) bool { return s.Day.GoImagesDemoComplete }) {
				return nil
			}
			buildID := stateValue(state, func(s *State) string { return s.Day.GoImagesDemoBuildID })
			if err := sb.PollPipelineComplete(ctx, buildID, secret); err != nil {
				return err
			}
			return state.update(ctx, func(s *State) {
				s.Day.GoImagesDemoComplete = true
			})
		},
	)
	complete := coordinator.NewIndicatorStep(
		"go-images.production-demo.complete",
		"✅ Production go-images demo complete",
		wait,
	)
	steps, err := complete.TransitiveDependencies()
	if err != nil {
		return nil, nil, err
	}
	wrapStepsWithStateFlush(steps, state, checkpoint)
	return steps, rs, nil
}

func cloneStringMap(values map[string]string) map[string]string {
	if values == nil {
		return nil
	}
	clone := make(map[string]string, len(values))
	for key, value := range values {
		clone[key] = value
	}
	return clone
}

func initializeState(ri *Input, rs *State) (*State, error) {
	if ri == nil || len(ri.Versions) == 0 {
		return nil, fmt.Errorf("no versions to release")
	}
	riChecksum, err := ri.checksum()
	if err != nil {
		return nil, fmt.Errorf("failed to checksum release input: %v", err)
	}
	if rs == nil {
		rs = &State{InputChecksum: riChecksum}
	} else if riChecksum != rs.InputChecksum {
		return nil, fmt.Errorf(
			"release input doesn't match initial input: expected checksum %v (from state), got %v (by calculation)",
			rs.InputChecksum,
			riChecksum,
		)
	}
	if rs.Versions == nil {
		rs.Versions = make(map[string]*VersionState)
	}
	for _, version := range ri.Versions {
		if _, ok := rs.Versions[version]; !ok {
			rs.Versions[version] = &VersionState{}
		}
	}
	return rs, nil
}

func wrapStepsWithStateFlush(steps []*coordinator.Step, state *stateAccess, checkpoint StateCheckpoint) {
	if checkpoint == nil {
		return
	}
	for _, step := range steps {
		run := step.Func
		step.Func = func(ctx context.Context) error {
			if err := state.flush(ctx); err != nil {
				return fmt.Errorf("flush pending release state before step: %w", err)
			}
			return run(ctx)
		}
	}
}

// CreateStepGraph creates the steps for a release of one or more versions of Microsoft build of Go. The
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
	return CreateStepGraphWithCheckpoint(ri, secret, rs, sb, nil)
}

// CreateStepGraphWithCheckpoint is like CreateStepGraph, and also records State after every
// meaningful mutation. External service calls are never made while the State lock is held.
func CreateStepGraphWithCheckpoint(
	ri *Input,
	secret *Secret,
	rs *State,
	sb ServiceBundle,
	checkpoint StateCheckpoint,
) ([]*coordinator.Step, *State, error) {
	var initializeErr error
	rs, initializeErr = initializeState(ri, rs)
	if initializeErr != nil {
		return nil, nil, initializeErr
	}
	state := &stateAccess{state: rs, checkpoint: checkpoint}

	createStatusReportIssue := coordinator.NewRootStep(
		"release-day.issue",
		"Create release day issue",
		shortTimeout,
		func(ctx context.Context) error {
			if stateValue(state, func(s *State) int { return s.Day.ReleaseIssue }) != 0 {
				return nil
			}
			issue, err := sb.CreateReleaseDayTrackingIssue(
				ctx, ri.TargetRepo, ri.RunnerGitHubUser, ri.Versions, secret)
			if err != nil {
				return err
			}
			return state.update(ctx, func(s *State) {
				s.Day.ReleaseIssue = issue
			})
		},
	)

	var versionCompleteSteps []*coordinator.Step
	var versionSpecificPublishSteps []*coordinator.Step

	for _, version := range ri.Versions {
		id := func(id string) string {
			return fmt.Sprintf("version.%s.%s", version, id)
		}
		name := func(n string) string {
			return fmt.Sprintf("%s, %s", n, version)
		}

		syncUpdate := coordinator.NewStep(
			id("upstream-commit"),
			name("⌚ Get upstream commit for release"),
			noTimeout,
			func(ctx context.Context) error {
				if stateValue(state, func(s *State) string { return s.Versions[version].UpstreamCommit }) != "" {
					return nil
				}
				commit, err := sb.PollUpstreamTagCommit(ctx, version)
				if err != nil {
					return err
				}
				return state.update(ctx, func(s *State) {
					s.Versions[version].UpstreamCommit = commit
				})
			},
			createStatusReportIssue,
		).Then(
			id("sync-pr"),
			name("Create sync PR"),
			shortTimeout,
			func(ctx context.Context) error {
				if stateValue(state, func(s *State) int { return s.Versions[version].UpdatePR }) != 0 {
					return nil
				}
				upstreamCommit := stateValue(state, func(s *State) string { return s.Versions[version].UpstreamCommit })
				pr, err := sb.CreateGitHubSyncPR(ctx, ri.TargetRepo, upstreamCommit, secret)
				if err != nil {
					return err
				}
				return state.update(ctx, func(s *State) {
					s.Versions[version].UpdatePR = pr
				})
			},
		).Then(
			id("sync-pr-merge"),
			name("⌚ Wait for PR merge"),
			microsoftGoPRCITimeout,
			func(ctx context.Context) error {
				if stateValue(state, func(s *State) string { return s.Versions[version].Commit }) != "" {
					return nil
				}
				pr := stateValue(state, func(s *State) int { return s.Versions[version].UpdatePR })
				commit, err := sb.PollMergedGitHubPRCommit(ctx, ri.TargetRepo, pr, secret)
				if err != nil {
					return err
				}
				return state.update(ctx, func(s *State) {
					s.Versions[version].Commit = commit
				})
			},
		).Then(
			id("azdo-sync"),
			name("⌚ Wait for AzDO sync"),
			internalMirrorTimeout,
			func(ctx context.Context) error {
				commit := stateValue(state, func(s *State) string { return s.Versions[version].Commit })
				return sb.PollAzDOMirror(ctx, ri.TargetAzDORepo, commit, secret)
			},
		)

		officialBuild := coordinator.NewStep(
			id("official-build-trigger"),
			name("🚀 Trigger official build"),
			shortTimeout,
			func(ctx context.Context) error {
				if stateValue(state, func(s *State) string { return s.Versions[version].OfficialBuildID }) != "" {
					return nil
				}
				buildID, err := sb.TriggerBuildPipeline(ctx, ri.MicrosoftGoPipeline, nil, nil, secret)
				if err != nil {
					return err
				}
				return state.update(ctx, func(s *State) {
					s.Versions[version].OfficialBuildID = buildID
				})
			},
			syncUpdate,
		).Then(
			id("official-build-wait"),
			name("⌚ Wait for official build"),
			microsoftGoOfficialCITimeout,
			func(ctx context.Context) error {
				buildID := stateValue(state, func(s *State) string { return s.Versions[version].OfficialBuildID })
				return sb.PollPipelineComplete(ctx, buildID, secret)
			},
		)

		testOfficialBuildCommit := coordinator.NewStep(
			id("innerloop-build-trigger"),
			name("🚀 Trigger innerloop build"),
			shortTimeout,
			func(ctx context.Context) error {
				if stateValue(state, func(s *State) string { return s.Versions[version].InnerloopBuildID }) != "" {
					return nil
				}
				buildID, err := sb.TriggerBuildPipeline(ctx, ri.MicrosoftGoInnerloopPipeline, nil, nil, secret)
				if err != nil {
					return err
				}
				return state.update(ctx, func(s *State) {
					s.Versions[version].InnerloopBuildID = buildID
				})
			},
			syncUpdate,
		).Then(
			id("innerloop-build-wait"),
			name("⌚ Wait for innerloop build"),
			microsoftGoInnerloopCITimeout,
			func(ctx context.Context) error {
				buildID := stateValue(state, func(s *State) string { return s.Versions[version].InnerloopBuildID })
				return sb.PollPipelineComplete(ctx, buildID, secret)
			},
		)

		readyForPublish := coordinator.NewIndicatorStep(
			id("artifacts-ready"),
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
			id("asset-metadata-download"),
			name("Download asset metadata"),
			shortTimeout,
			func(ctx context.Context) error {
				buildID := stateValue(state, func(s *State) string { return s.Versions[version].OfficialBuildID })
				dir, err := sb.DownloadPipelineArtifactToDir(
					ctx,
					buildID,
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
			id("artifacts-download"),
			name("Download artifacts"),
			shortTimeout,
			func(ctx context.Context) error {
				buildID := stateValue(state, func(s *State) string { return s.Versions[version].OfficialBuildID })
				var err error
				artifactsDir, err = sb.DownloadPipelineArtifactToDir(
					ctx,
					buildID,
					"Binaries Signed",
					secret,
				)
				return err
			},
			officialBuild,
		)

		githubPublish := coordinator.NewStep(
			id("github-tag"),
			name("🎓 Create GitHub tag"),
			shortTimeout,
			func(ctx context.Context) error {
				if stateValue(state, func(s *State) string { return s.Versions[version].GitHubTag }) != "" {
					return nil
				}
				tag := fmt.Sprintf("v%s", version)
				commit := stateValue(state, func(s *State) string { return s.Versions[version].Commit })
				err := sb.CreateGitHubTag(ctx, version, ri.TargetRepo, tag, commit, secret)
				if err != nil {
					return err
				}
				return state.update(ctx, func(s *State) {
					s.Versions[version].GitHubTag = tag
				})
			},
			readyForPublish,
		).Then(
			id("github-release"),
			name("🎓 Create GitHub release"),
			shortTimeout,
			func(ctx context.Context) error {
				if stateValue(state, func(s *State) string { return s.Versions[version].GitHubRelease }) != "" {
					return nil
				}
				tag := stateValue(state, func(s *State) string { return s.Versions[version].GitHubTag })
				err := sb.CreateGitHubRelease(ctx, ri.TargetRepo, tag, assetJSONPath, artifactsDir, secret)
				if err != nil {
					return err
				}
				return state.update(ctx, func(s *State) {
					s.Versions[version].GitHubRelease = tag
				})
			},
			downloadAssetMetadata, downloadArtifacts,
		)

		akaMSPublish := coordinator.NewStep(
			id("akams-update"),
			name("🎓 Update aka.ms links"),
			shortTimeout,
			func(ctx context.Context) error {
				buildID := stateValue(state, func(s *State) string { return s.Versions[version].AkaMSBuildID })
				if buildID == "" {
					var err error
					buildID, err = sb.TriggerBuildPipeline(ctx, ri.MicrosoftGoAkaMSPipeline, nil, nil, secret)
					if err != nil {
						return err
					}
					if err := state.update(ctx, func(s *State) {
						s.Versions[version].AkaMSBuildID = buildID
					}); err != nil {
						return err
					}
				}
				if !stateValue(state, func(s *State) bool { return s.Versions[version].AkaMSUpdated }) {
					if err := sb.PollPipelineComplete(ctx, buildID, secret); err != nil {
						return err
					}
					return state.update(ctx, func(s *State) {
						s.Versions[version].AkaMSUpdated = true
					})
				}
				return nil
			},
			readyForPublish, downloadAssetMetadata,
		)

		dockerfilePublish := coordinator.NewStep(
			id("dockerfiles-update"),
			name("Update Dockerfiles"),
			// Set timeout to expect one CI run per version. This accounts for the worst case: each
			// version contributes a Dockerfile update to the shared PR just before CI finishes.
			microsoftGoImagesPRCITimeout*time.Duration(len(ri.Versions)),
			func(ctx context.Context) error {
				imageUpdatePR := stateValue(state, func(s *State) int { return s.Versions[version].ImageUpdatePR })
				if imageUpdatePR == 0 {
					var err error
					imageUpdatePR, err = sb.CreateDockerImagesPR(ctx, ri.TargetGoImagesRepo, assetJSONPath, "", secret)
					if err != nil {
						return err
					}
					if err := state.update(ctx, func(s *State) {
						s.Versions[version].ImageUpdatePR = imageUpdatePR
					}); err != nil {
						return err
					}
				}
				if !stateValue(state, func(s *State) bool { return s.Versions[version].ImagesUpdated }) {
					_, err := sb.PollMergedGitHubPRCommit(ctx, ri.TargetGoImagesRepo, imageUpdatePR, secret)
					if err != nil {
						return err
					}
					return state.update(ctx, func(s *State) {
						s.Versions[version].ImagesUpdated = true
					})
				}
				return nil
			},
			readyForPublish, downloadAssetMetadata,
		)
		versionCompleteSteps = append(versionCompleteSteps, coordinator.NewIndicatorStep(
			id("microsoft-go-publish-complete"),
			name("✅ microsoft/go publish and go-images PR complete"),
			githubPublish,
			akaMSPublish,
			dockerfilePublish,
		))

		azureLinuxPRPublish := coordinator.NewStep(
			id("azure-linux-pr"),
			name("🚀 Trigger Azure Linux PR creation"),
			shortTimeout,
			func(ctx context.Context) error {
				buildID := stateValue(state, func(s *State) string { return s.Versions[version].AzureLinuxUpdateBuildID })
				if buildID == "" {
					var err error
					buildID, err = sb.TriggerBuildPipeline(ctx, ri.AzureLinuxCreatePRPipeline, nil, nil, secret)
					if err != nil {
						return err
					}
					if err := state.update(ctx, func(s *State) {
						s.Versions[version].AzureLinuxUpdateBuildID = buildID
					}); err != nil {
						return err
					}
				}
				if !stateValue(state, func(s *State) bool { return s.Versions[version].AzureLinuxPRSubmitted }) {
					if err := sb.PollPipelineComplete(ctx, buildID, secret); err != nil {
						return err
					}
					return state.update(ctx, func(s *State) {
						s.Versions[version].AzureLinuxPRSubmitted = true
					})
				}
				// Note: we don't keep track of the PR inside this process because it may take
				// an arbitrary time to get approval to merge.
				return nil
			},
			readyForPublish,
		)

		versionSpecificPublishSteps = append(versionSpecificPublishSteps, coordinator.NewIndicatorStep(
			id("external-publish-complete"),
			name("✅ External publish complete"),
			azureLinuxPRPublish,
		))
	}

	versionsComplete := coordinator.NewIndicatorStep(
		"versions.publish-complete",
		"✅ All microsoft/go publish and go-images PRs complete",
		versionCompleteSteps...,
	)

	imagesReady := coordinator.NewStep(
		"images.commit",
		"Get go-images commit",
		shortTimeout,
		func(ctx context.Context) error {
			commit := stateValue(state, func(s *State) string { return s.Day.GoImagesCommit })
			if commit == "" {
				var err error
				commit, err = sb.PollImagesCommit(ctx, ri.Versions, secret)
				if err != nil {
					return err
				}
				if err := state.update(ctx, func(s *State) {
					s.Day.GoImagesCommit = commit
				}); err != nil {
					return err
				}
			}
			return sb.PollAzDOMirror(ctx, ri.TargetAzDOGoImagesRepo, commit, secret)
		},
		versionsComplete,
	).Then(
		"images.build-trigger",
		"🚀 Trigger go-image build/publish",
		shortTimeout,
		func(ctx context.Context) error {
			if stateValue(state, func(s *State) string { return s.Day.GoImagesOfficialBuildID }) != "" {
				return nil
			}
			buildID, err := sb.TriggerBuildPipeline(ctx, ri.MicrosoftGoImagesPipeline, nil, nil, secret)
			if err != nil {
				return err
			}
			return state.update(ctx, func(s *State) {
				s.Day.GoImagesOfficialBuildID = buildID
			})
		},
	).Then(
		"images.build-wait",
		"⌚ Wait for go-image build/publish",
		microsoftGoImagesOfficialCITimeout,
		func(ctx context.Context) error {
			buildID := stateValue(state, func(s *State) string { return s.Day.GoImagesOfficialBuildID })
			return sb.PollPipelineComplete(ctx, buildID, secret)
		},
	).Then(
		"images.version-check",
		"🌊 Check published image version",
		// This may need to be expanded to deal with MAR latency.
		// Alternatively, the go-images build can wait: https://github.com/microsoft/go/issues/1258
		shortTimeout,
		func(ctx context.Context) error {
			if stateValue(state, func(s *State) bool { return s.Day.MARVersionChecked }) {
				return nil
			}
			if err := sb.CheckLatestMARGoVersion(ctx, ri.Versions); err != nil {
				return err
			}
			return state.update(ctx, func(s *State) {
				s.Day.MARVersionChecked = true
			})
		},
	)

	createBlog := coordinator.NewStep(
		"announcement.create",
		"📰 Create blog post markdown",
		shortTimeout,
		func(ctx context.Context) error {
			if stateValue(state, func(s *State) bool { return s.Day.AnnouncementWritten }) {
				return nil
			}
			if err := sb.CreateAnnouncementBlogFile(ctx, ri.Versions, ri.RunnerGitHubUser, ri.Security, secret); err != nil {
				return err
			}
			return state.update(ctx, func(s *State) {
				s.Day.AnnouncementWritten = true
			})
		},
		versionsComplete, imagesReady,
	)

	completeStep := coordinator.NewIndicatorStep(
		"release.complete",
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
	wrapStepsWithStateFlush(allSteps, state, checkpoint)
	return allSteps, rs, nil
}
