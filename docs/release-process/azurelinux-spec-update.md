# Azure Linux spec file update instructions

This document describes how to update the Azure Linux 3.0 spec file to use a new version of Go.

For each version released:

1. Find and open the [microsoft-go](https://dev.azure.com/dnceng/internal/_build?definitionId=958) build that produced the new Microsoft Go release.
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

## Resources

The Azure Linux PR generation implementation is in [/cmd/releasego/update-azure-linux.go](../../cmd/releasego/update-azure-linux.go).

These are the Azure Linux pipelines involved in finishing up the PR:

* [Source Tarball Publishing](https://dev.azure.com/mariner-org/mariner/_build?definitionId=2284)
* [Buddy Build](https://dev.azure.com/mariner-org/mariner/_build?definitionId=2190)

## Improvements

We intend to incorporate this process into release automation.
See https://github.com/microsoft/go-lab/issues/79.
