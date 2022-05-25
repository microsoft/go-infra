# Release process

We have a handful of pipelines to manage the steps creating a release.

The goal is to have this system handle the easy stuff that's easy to mistype or forget, and that's annoying to copy-paste.

All steps written below are performed by the release pipeline (not by the dev) unless stated otherwise.

See [instructions.md](instructions.md) for more info about how to use this infrastructure.

## Status

This doc describes the plan to create Azure Pipelines that run the release steps for the Microsoft build of Go. The doc and the plan are works in progress. The doc is written so that it describes the final state of the pipelines, and this status section can simply be removed once the entire doc is implemented.

The overall effort is tracked by <https://github.com/microsoft/go/issues/423>.

* (3) release-go
    * ✅ aka.ms link generation <https://github.com/microsoft/go/issues/453>
    * ✅ GitHub release generation
    * ✅ Tag generation

# Pipelines

## (1) release-start

Inputs:
* A list of `major.minor.patch-release[-note]` release numbers.
    * Example: 1.17.8-1, 1.17.8-1-fips, 1.18.0-1, 1.18.0-1-fips

Steps:
1. Create a tracking issue for the release event, and one for each release version number.
1. Launch a "(2) release-build" for each release/issue.
1. Launch one "(4) release-go-images" that includes all release version numbers.
    * This pipeline has to wait for (2) to finish. We just launch it now while we have all the info necessary to kick it off.

## (2) release-build

Inputs:
* microsoft/go release issue number.
* A single `major.minor.patch-revision[-note]` release number.
    * The note determines whether this is a boring/FIPS release.
* (Optional) One or more IDs to start polling.
    1. microsoft/go PR number.
    1. microsoft/go commit hash created by PR merge.
    1. AzDO Go pipeline build id.
    1. microsoft/go-images PR number.
    * The last one defined "wins", and the job starts by polling that. This means if the dev needs to re-trigger this job after a failure, they can simply fill in the last parameter the build was stuck on.

Steps:
1. Poll the upstream Go repository for the release availability.
    * If a normal release branch, fail immediately if the tag isn't available.
    * If boring/FIPS, poll the RELEASES file.
1. Create an auto-update PR updating to the specific commit released by upstream.
1. Poll CI and merge status for a green merge.
1. Poll the official Go pipeline to check for an official build triggered by the auto-update commit being mirrored.
1. Poll the build for successful completion.
1. Launch "(3) release-go" on the result.
1. If this branch has an associated go-images Dockerfile:
    1. Create an auto-update PR for go-images based on the build.
    1. Poll CI and merge status for a green merge.

If any polling steps fail (or time out), the pipeline notifies the dev handling the release by commenting on the release issue.

## (3) release-go

Inputs:
* microsoft/go release issue number.
* A single `major.minor.patch-revision[-note]` release number.
* AzDO build ID: the microsoft/go build to release.
    * If running this job manually, note: this is the id in the URL, not the build number.

Steps:
1. Check that results match the expected Go version.
    * VERSION file, commit hashes. Goal: prevent the wrong build from being passed into this pipeline.
1. Tag the commit on GitHub.
1. Add a GitHub release on the tag. Attach the source archive files and assets json.
1. Update aka.ms links.
1. Add a comment to the release issue alerting the dev that the process has completed.

## (4) release-go-images

Inputs:
* A list of major.minor.patch release numbers.

Steps:
1. Poll the latest `microsoft/main` go-images commit in AzDO to ensure its set of versions matches all target versions.
1. Run the go-images internal build pipeline in "build" mode.
1. Manual approval step: check the set of tags that were built.
    * The dev should check to make sure the image build filter is set up properly and built the right set of tags. Not every image in go-images is necessarily built during a given servicing release.
1. Run the go-images internal build pipeline in "publish" mode.

# Considerations for improvement

## Polling

This plan has a lot of polling, and that's bad! It keeps an agent busy while it does effectively nothing. With sufficiently large pools, this may not end up causing problems, but it's a waste in any case. If this ends up being a problem, we have some workarounds in mind:

* (Bad) Add "user approval" steps on a server-side job to let the pipeline stop without using up a VM.
    * This means we don't keep a machine busy polling. However, it puts a burden on the dev running the release.
* (Best) Move the polling to an external system that doesn't rely on an AzDO build pool.
    * We can have the pipeline call out to Azure Functions (potentially Durable Functions) to have it start polling and report back later.
    * This could be implemented using an [agentless/server job](https://docs.microsoft.com/en-us/azure/devops/pipelines/process/phases?view=azure-devops&tabs=yaml#server-jobs), with:
        * [AzureFunction@1](https://docs.microsoft.com/en-us/azure/devops/pipelines/tasks/utility/azure-function?view=azure-devops) - Kick off the polling function.
        * [ManualValidation@0](https://docs.microsoft.com/en-us/azure/devops/pipelines/tasks/utility/manual-validation?view=azure-devops&tabs=yaml) - Receive the callback in the form of a service account pressing the approve button.
* (Incredible) Add or discover some Azure Pipelines functionality that enables some way to poll without a constantly reserved agent.

## Release notes

We should consider where release notes should go that are specific to the Microsoft build of Go. In particular, FIPS-related changes.

It would be reasonable to have a place where release notes can be checked in: doc/go1.18-fips.html? Inside patch file descriptions with conventions to extract relevant info? Then the release-go pipeline can detect the notes, format them, and put them into e.g. the GitHub release.

## Use more AzDO features: stage/job retries, release pipelines

These pipelines don't take advantage of the "retry failed jobs" or stage retry features of AzDO. Instead, we queue a new build with configuration to make it avoid re-running any steps that were already completed. There are a few reasons the AzDO retry logic doesn't seem suitable for these pipelines:

* AzDO retry granularity is at the job or stage level, not step level. Our steps are not idempotent: running them a second time would fail.
    * We could make each step idempotent, but it would be considerably more complex.
    * We could split up steps into multiple jobs or stages, but acquiring an agent can take a while, and we would be multiplying that time.
        * Running parallel jobs/stages could mitigate this, but a significant amount of the release pipeline must be sequential. (In particular **(2) release-build**.)
* There is no way to change variables/parameters for an AzDO retry.
    * The microsoft/go-infra commit the pipeline uses for the YAML pipeline cannot be changed. There is also no opportunity to change the input parameters and variables.
    * Example modifications that will sometimes be necessary:
        * A pipeline YAML fix.
        * go-infra tooling fix.
        * Polling a fixed PR number submitted by a dev rather than a broken PR that will never merge.

Instead, the pipelines are written to be as easy to re-run with modified parameters as possible. Usually a retry involves copy-pasting a single number into the "run new build" dialog. See [instructions.md#retrying](instructions.md#retrying)

Another AzDO feature we aren't using are [Release Pipelines](https://docs.microsoft.com/en-us/azure/devops/pipelines/release/?view=azure-devops). These have more flexiblity to modify and retry in the middle of a release that's running. However, there is no YAML (source-controlled) workflow, and they are called "classic" now, so we don't want to add new dependencies on them.

If more AzDO features are added in the future that overcome these limitations, we should use them.
