# Azure Linux spec file update instructions

This document describes how to update the Azure Linux 3.0 spec file to use a new version of Go.

## Prerequisites

Join [azurelinuxde-frja](https://coreidentity.microsoft.com/manage/Entitlement/entitlement/azurelinuxde-frja) with `Azl-Collaborator` role.

Example justification: `Maintaining Microsoft build of Go Azure Linux package`

## Manual workflow

For each version released:

1. Find and open the [microsoft-go](https://dev.azure.com/dnceng/internal/_build?definitionId=958) build that produced the new Microsoft build of Go release.
1. From the URL, copy the build ID.
   * For example, in `[...]results?buildId=2535309&view=results`, it is `2535309`.
1. Go to [microsoft-go-infra-update-azure-linux](https://dev.azure.com/dnceng/internal/_build?definitionId=1405).
1. Click "Run pipeline", and:
   1. For `The ID of the microsoft-go build pipeline`, paste the build ID.
   1. Fill other fields as instructed in the dialog.
   1. Click "Run".
1. Wait for the pipeline to complete.
1. Go to the generated PR: either look in the pipeline's logs for the link or check your GitHub notifications.
1. Follow the instructions in the PR to finalize it.
1. Done!

## Semi-automated workflow

This workflow uses tools to avoid time consuming copy paste and manual CI retry after tarball mirroring.

1. Use https://github.com/microsoft/go-lab/tree/main/goaztool to mirror the source tarballs to Azure Linux storage.
   * For example, `go run github.com/microsoft/go-lab/goaztool/cmd/azlmirror -versions 1.24.5-1,1.25.3-1`
   * Make sure to do this first, so you don't have to manually re-run the GitHub CI pipelines later.

Follow manual workflow to create PRs:

1. Find and open each microsoft-go build...
1. ... See above manual workflow ...
1. Wait for each microsoft-go-infra-update-azure-linux pipeline to complete.

Ignore instructions in each PR, and instead:

1. Use https://github.com/microsoft/go-lab/tree/main/goaztool to create a buddy build for each PR:
   1. For example, `go run github.com/microsoft/go-lab/goaztool/cmd/azlbuddy -prs 1234`
      * Replace `1234` with the PR URL or number.
   1. Paste a link to the buddy build from the command output into the PR conversation.
   1. Mark the PR ready to review.
1. Done!

## Automated workflow

Tracked by [microsoft/go-lab#79](https://github.com/microsoft/go-lab/issues/79).

## Resources

The Azure Linux PR generation implementation is in [/cmd/releasego/update-azure-linux.go](../../cmd/releasego/update-azure-linux.go).

These are the Azure Linux pipelines involved in finishing up the PR:

* [Source Tarball Publishing](https://dev.azure.com/mariner-org/mariner/_build?definitionId=2284)
* [Buddy Build](https://dev.azure.com/mariner-org/mariner/_build?definitionId=2190)

## Improvements

We intend to incorporate this process into release automation.
See https://github.com/microsoft/go-lab/issues/79.
