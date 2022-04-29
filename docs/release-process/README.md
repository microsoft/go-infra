# Release process

We have a handful of pipelines to manage the steps creating a release.

The goal is to have this system handle the easy stuff that's easy to mistype or forget, and that's annoying to copy-paste.

All steps written below are performed by the release pipeline (not by the dev) unless stated otherwise.

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
